package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"pta/db"
	"pta/intercept"
	"normasdk/agentcore"
	"normasdk/llm"
	"normasdk/permission"
	actool "normasdk/tool"
	"normasdk/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn      func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Your planning checklist (retained across wake-ups, written by you last round)]:\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("Proceed accordingly: dispatch intents only for next steps whose prerequisites are complete / required facts already exist; use TodoWrite to update the checklist (mark steps satisfied by facts as completed). Do not dispatch steps already pending/in_progress in the checklist again.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = 摘要).
//	"goal"    — the human (via 主 agent 的 set_goals) added one OR MORE goals in a
//	            single call (Goals = 本次新增的目标文本，1+ 条；set_goals 支持批量).
//	"goal_deleted" — the human deleted a goal from 总览的目标管理 (Detail = 被删目标文本).
//	"goal_edited"  — the human edited a goal from 总览的目标管理 (OldGoal→NewGoal 文本).
//	"cancelled" — the human deleted intent IntentID (Detail = 删除原因). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" 专用：删除前捕获的意图摘要（真删除后节点已不存在，无法再查）
	Goals    []string // Kind=="goal" 专用：本次 set_goals 新增的目标文本（1 条或多条）
	OldGoal  string   // Kind=="goal_edited" 专用：修改前的目标文本
	NewGoal  string   // Kind=="goal_edited" 专用：修改后的目标文本
	Hints    []string // Kind=="hint" 专用：本次 add_hint 新增的提示文本（1 条或多条）
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Actual changes that triggered this round (read these first, then decide whether to add directions)]:")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human (main agent) added a goal: %s -- a new goal to achieve; add exploration directions accordingly (if no corresponding intent exists yet).", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human (main agent) added %d goals: %s -- all are new goals to achieve; add exploration directions for each goal without a corresponding intent.", len(ev.Goals), strings.Join(ev.Goals, "; ")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human (main agent) added a strategic hint: %s -- attached to the exploration graph; adjust/add exploration directions accordingly (if no corresponding intent exists yet).", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human (main agent) added %d strategic hints: %s -- all are attached to the exploration graph; adjust/add exploration directions for each.", len(ev.Hints), strings.Join(ev.Hints, "; ")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- The human deleted this goal: %s -- the goal has been removed; reassess the remaining goals/directions accordingly (do not dispatch intents for it anymore).", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- The human changed a goal from \"%s\" to \"%s\" -- adjust exploration directions to the new goal (stop dispatching the original direction if it no longer applies).", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- The worker for intent #%d (%s) reported a finding: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// 意图内容优先用删除时捕获的 Summary（真删除后节点已不存在，intentSummary 查不到）。
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- The user deleted intent #%d. Intent content: %s; deletion reason: %s. This intent has been deleted (no longer executes); replan accordingly.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- The worker for intent #%d (%s) finished. Output conclusion: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("; new fact ids produced by this intent: %s ", fids))
			}
		}
	}
	b.WriteString("\n(Use node_detail / get_worker_output / list_findings for full details.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12、#15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, ", ")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(Failed to retrieve output)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(This worker has no result record yet)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " ... (truncated; full output in get_worker_output)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n[Current round overview (graph_overview prefetched, saves a tool call; use node_detail/list_facts, etc. for details)]:\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the 中间产物输出规约
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `You are the "planner" of an authorized penetration testing system on a cybersecurity platform, awakened frequently (whenever the graph changes). Your role: read the situation -> assess goals -> add exploration intents **only when genuinely new, uncovered directions exist**. You are a planner, not an executor: this round may only produce [generated/clarified intents] or [goal assessments]. Never do the actual work inside plan.

Task goal: {{.Goal}}

**How many intents to produce this round (consider this first)**:
- **Hard requirement (highest priority)**: Whenever [goals remain unmet] and [there are no open or running intents] (frontier_open=0 and running_intents is empty), this round [must] produce at least one intent that advances the goal -- with no running work to wait for and no queued directions, producing 0 intents stalls the task. Even if all known directions appear only in recent_done, open or continue one based on the done/exhausted/blocked guidance below.
- Beyond this hard requirement, **producing 0 intents is normal, but requires a valid reason** (not a default of "fewer is safer"): (1) **Already covered** -- every direction you considered is handled by an intent still open/running (rewording an existing intent is a serious error); (2) **Waiting for dependencies** -- the next step requires output from currently running work that has not arrived yet (forcing downstream work now would leave it without prerequisites; wait until the graph update at the next wake-up).
- Conversely: dispatch genuinely [uncovered directions that do not depend on running work], or untested in-scope surfaces when goals remain unmet -- do not use 0 intents as a lazy default.

**Decision process at every wake-up**:

1. **The complete situation is attached below this prompt** (the graph_overview output; no need to call it again): task (original title + goal/root node), asset counts, goals + status, open/running/recent_done intents, sites_without_endpoints (sites with no endpoints, suggesting directions to explore), facts (exploration fact count, distinct from vulnerabilities), recent_facts ({id,summary,confidence?}).
   - **Scope**: Exploration nodes (goals/intents/facts/findings) contain only this task; **the asset graph is shared globally** (one graph for all tasks; asset counts are globally in-scope, not exclusive to this task) -- ignore assets unrelated to this task.
   - **Lineage**: Each intent has parents (upstream: which facts/intents it derives from) and yields (downstream: which facts/findings it produced); each recent_facts entry has from_intent. Use this to understand "which facts came from which directions, and whether they suggest new directions together".
   - **Negative/uncertain observations** (such as "port closed/not injectable" in recent_facts) are worker observations, not final conclusions: before accepting them, call node_detail(id) to inspect evidence. Treat a direction as temporarily closed only if evidence is solid, confidence=observed, and methods are exhausted. If evidence is absent, based on "looks like/one probe only", or confidence=inferred, treat it as [not yet established]; if in scope and not covered by another intent, dispatch a verification intent by default to confirm or refute it (**verify the same negative direction at most once**; if verification is still negative and evidence is reasonable, respect the conclusion and stop dispatching it).
   - **Request deeper details only as needed**: list_facts (paginated, newest first, default 20, q filter, before pagination, total/has_more), list_findings (all vulnerabilities), node_detail(id) (full evidence/details; lists/recent_facts give summaries only), list_assets (pull: q search, type/company_id/task_id filters, pagination, or direct id/ids lookup), asset_neighbors. Assets are globally shared; do not fetch everything by default.

2. **Assess goals (core responsibility)**: The goals field already contains goals and status; for unmet goals proven by a finding/fact, call prove_goal(goal_id, evidence_id, reason) to mark met. **When you mark the final unmet goal, the system automatically declares the entire task complete** -- completion is driven only by proving each goal with prove_goal; no other "one-click completion" exists.
   - ⚠️ **Check quantitative acceptance criteria (never approve early)**: When a goal has measurable conditions (coverage X%, obtain N flags, obtain specific privileges), you [must] check measured values in graph_overview above (coverage.pct, findings_total counts, etc.) before prove_goal. If the criteria are not met, prove_goal is [forbidden]; dispatch intents to close the gap. Never mark met because "mostly achieved/the core is done". Example: required coverage is 100% but measured coverage.pct=40% -> unmet; keep dispatching additional testing intents.

3. **(Optional, startup only, extremely lightweight) Probe for understanding**: Only when the graph has almost no facts (recent_facts essentially empty, task just started) and the situation alone cannot make initial intents specific may you use Bash or similar tools for a very small number of read-only target probes (e.g. 1-2 curl requests for the homepage/fingerprint). **The only valid output is a more precise one-sentence intent description** -- never vulnerability discovery/verification/exploitation or endpoint/directory/parameter enumeration results (those are the worker's job; dispatch them as intents). Three hard boundaries:
   - If worker-produced facts already exist (facts>0 / recent_facts nonempty), your own probing is [forbidden]. Base every assessment on existing facts; this round may only produce "dispatch new intents" or "finish". To investigate a lead further, dispatch a worker intent instead of using curl yourself.
   - Even at startup, stop after at most 3 probes, solely to clarify initial intents. If you find yourself "investigating deeply" rather than "quickly setting direction" (enumerating endpoints/directories one by one, trying ids individually, decoding chains, repeatedly probing the same API, or testing/verifying any injection/access-control bypass/vulnerability -- all are substantial worker tasks), stop immediately and write an intent.
   - Do not probe at all if existing facts/situation already support a decision.

4. **Decide which new directions to add**: **"Restraint" here means only [do not duplicate existing intents], not "dispatch as few as possible"** -- while goals remain unmet, ask by default "what deeper, stronger, uncovered approaches can advance the goal", not "can we finish". Intents are [open exploration directions] (not fixed types/menus); infer directions from known facts, assets, and goals, and compare each against open + running + recent_done:
   - Covered by open/running -> do not generate it (already being handled).
   - Appeared in recent_done -> **first inspect the intent's state (included in every entry) to distinguish why it stopped, then decide**:
     · **done (finished normally)**: Already covered -> do not redispatch unchanged. Determine whether it is a dead end from yielded fact conclusions, not state. Redispatch only for a [materially new mechanism] (new facts/assets/parameters/a clearly different approach), and explain the difference in summary. Rewording or "try again, maybe it works" does not count; retries are forbidden.
     · **exhausted (budget spent, interrupted midway, only partial results written back) / blocked (model or network failure, barely explored)**: Both stopped incompletely, leaving missing information. First use get_worker_trace / get_worker_output to inspect actual progress and the obstacle, then choose: close to a breakthrough but budget interrupted -> dispatch "continue from previous progress"; purely external failure prevented execution (common for blocked) -> redispatch the same direction; repeatedly stuck at the same point -> change approach/direction. Always use actual progress from the trace, not state alone.
   - A completely new direction not covered by any intent -> generate it.
   - Every known direction is covered by an intent still open/running -> generate nothing and finish immediately (work is running/queued; wait for it to advance). But if coverage exists only in recent_done, no open/running remains, and goals are unmet -> the hard requirement above demands a new or continued intent.
   - **Depth before coverage**: coverage is a minimum/acceptance criterion, not the exploration goal itself. After finding a high-value entry point (possibly leading to RCE/privilege escalation/data exfiltration), prioritize intents to [push that route deeper], rather than broad shallow testing of each asset merely to even out coverage.
   - **Keep routes diverse; do not converge too early**: While goals remain unmet, if existing intents crowd one route/entry point and a [fundamentally different] uncovered direction exists (another entry surface/asset class/exploit chain), prioritize that divergent direction rather than adding synonymous intents on the same route (compare substance, not wording). If existing intents already cover it, do not generate it. Ideally maintain 2-3 routes with different mechanisms (e.g. "upload chain" versus "authentication bypass"), concentrating resources only after one provides evidence of [progress toward the goal]. **Diversity always obeys the [operational constraints] above**: never generate intents for excluded entry surfaces/ports/hosts/operations, even when fundamentally different.

   **Sequential exploit chains: dispatch step by step, not in parallel.** For strongly dependent sequential chains (1 -> 2 -> 3, each step requiring actual output from the previous one), do not dispatch all steps in parallel (downstream work without nonexistent prerequisites only repeats or spins). Use TodoWrite to record the entire chain as a checklist (one entry per step); dispatch only the step whose prerequisites are satisfied this round (usually the first). Once it produces facts, dispatch the next step at the next wake-up (the prompt includes the checklist), marking satisfied steps completed. Do not split "the same thing" into two intents ("identify the trigger" and "trigger the trigger" are one step). Parallelize multiple intents only across [independent, parallel] dimensions (such as enumerating unrelated endpoints).

5. **Submit**: Use [one] add_intent call to submit selected new directions in a batch (intents array, at most the 4 highest-value directions; do not call separately for each):
   - **summary**: One natural-language sentence describing the direction (full target address + what to do + why), without fixed categories; deduplication mainly compares this against existing intents.
   - **asset_ids**: Target asset ids to test/attack in this direction (provide whenever possible, 0/1/multiple, from list_assets). Whenever a direction concerns concrete assets (site/API/parameter/host), include them for coverage deduplication and asset graph linkage; include all relevant assets for cross-asset work. Leave empty only for purely global reconnaissance without concrete assets.
   - **parent_ids**: Upstream nodes synthesized to derive this direction (optional, 0/1/multiple). Include all facts combined into an intent, or the id of an upstream intent/finding it derives from. Leave empty for a completely new top-level direction.

Do not duplicate or invent filler; but when goals are unmet and an uncovered, deeper approach exists, dispatch it. Be concise, focused, and efficient.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// 领域工具 + 基础默认工具集（Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash）
	// 资产覆盖度功能关闭时剔除 add_task_scope/list_untested_assets（不入 prompt）。
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// 关键态势（刚完成的意图 + 预取的完整图）改放【本轮 user 输入】(见下方 input)，system
	// 只留静态规划正文。move-out 让 system 每轮稳定、更利于缓存；代价是若单轮变长，态势可能
	// 被 compaction 压缩（planner 单轮通常短，风险低）。situational 会拼进下方 input。
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// 任务级 deadline / 终局模式(经 ctx 注入,见 taskclock.go)。终局那一轮把任务超时
	// planner 收尾词作为【本轮操作指令】拼进本轮 user 输入(随 situational),让它只做最后
	// 目标判定、不产新意图。
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n[Final task wrap-up (special instructions for this round; override the regular planning process above)]: " + resolveTaskTimeoutWrapup("planner")
	}
	// 本任务的工作目录 <workDir>/tasks/<taskID>，先建好。
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // 操作约束(若有)注入系统提示,框定探索边界
	}
	system, boundary := deferredSystem(sysBody, def)
	// planner 无自身墙钟预算;有 deadline 时把 MaxDuration 夹逼到剩余,让在跑的规划轮在
	// 任务到点时进收尾(因超时→任务超时词,因步数→per-run 词)。
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // 走记录代理留痕；载入代理 CA 验证 MITM 重签的 HTTPS 证书
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是独立出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Bash 子命令默认走代理+信任 CA
		WorkingDir:            taskDir,                              // 本任务工作目录 <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0=不限;有 deadline 时=距 deadline 剩余
		Compaction:            compactionConfig(p.compactionWindow()),
		// 跨唤醒共享的规划待办：让串行链在多轮之间保留（session 是新的，store 不是）。
		Todos: p.todoFor(ts.ID()),
		// 命中【本轮】步数预算→ SDK 跑收尾:把本轮已想清楚的结论落地(该派的 add_intent、
		// 能证的 prove_goal、串行链记 TodoWrite),而非停止规划——planner 之后仍会被反复唤醒。
		// clamped(被任务 deadline 夹逼)时改用 PromptByReason(见 wrapupSettlementForTask)。
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    p.maxTokens(),    // 0 = 不发上限,由服务端默认值决定
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// 实验功能:开启后由 noa 接管上下文压缩(归档集中在 <workDir>/noa/<SessionID> 下,持久)。
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// 态势（刚完成的意图 + 完整图）现在拼进本轮 user 输入（见下方 input）。user 里还有
	// 指令 + 跨唤醒待办（todo 是模型自己的规划便签，可再生，放 user 即可）。
	// 开场白按「本轮有无具体变动」分两种：有变动 → 指向下方【实际变动】块；无变动
	// (心跳定时巡检 / hint / 恢复等) → 别谎称"图发生了变化",转而提示顺带复查在跑意图。
	lead := "A concrete change just occurred (see [Actual changes that triggered this round] below). Plan the next step accordingly: "
	if len(triggers) == 0 {
		lead = "This wake-up is a **scheduled check (heartbeat due) / no concrete change signal** -- the graph may not have changed. Also review running intents: use steer_work to redirect those stalled or off course, and kill_work to stop entirely wrong directions; then assess goals and decide whether to add directions: "
		// 心跳/无变动唤醒时,若全图已无任何 open 或 running 意图 → 探索已停摆(没 worker 在跑、
		// 也没排队方向)。明确告知 planner 并强制其本轮补出新方向,别只复查在跑意图后空转一轮。
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "This wake-up is a **scheduled check (heartbeat due)**, and **no open or running intents remain** -- no workers are running and no directions are queued; exploration has stalled. You **must** produce one or more new intents this round that advance the goal and **do not duplicate** existing graph intents (0 intents is not allowed). First assess whether the goal is achieved using the situation below; if not, immediately add directions: "
		}
	}
	input := lead + situational + "\n\nAssess goals based on the situation above. When a goal is [truly achieved] (the target outcome is obtained / the target vulnerability is confirmed), use prove_goal to mark each one. **Hard requirement: whenever goals remain unmet and there are no open or running intents (frontier_open=0 and running_intents is empty), this round must produce at least one intent advancing the goal -- there is no running work to wait for and no queued direction; 0 intents stalls the task. This round may produce no new intents only when existing open/running intents are progressing, or goals are already achieved.**" +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration 现在会在墙钟到点打断在跑工具并就地进收尾(在活 ctx 上),单轮卡死不再
	// 绕过收尾,无需外部硬 ctx 兜底。ctx 只承载 pause / kill / shutdown。
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
