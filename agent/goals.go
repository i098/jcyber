package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"pta/db"
	"normasdk/agentcore"
	"normasdk/llm"
	acperm "normasdk/permission"
	actool "normasdk/tool"
	"normasdk/transcript"
)

// goalsDefaultTmpl is the built-in EDITABLE body (段 [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `You are a penetration testing goal decomposer. Your responsibility is to identify the **final outcomes to achieve** from user input, not to plan attack steps.

**First step (before decomposing goals): extract operational constraints**
Identify explicit operator rules about [which operations are allowed and prohibited] in the "task goal / task description", and call set_constraints to register each one (skip extraction if the description and goal contain no operational constraints):
- type=deny: prohibited operations (e.g. "do not scan ports", "do not write/delete in production", "no brute force", "do not touch a certain subdomain").
- type=allow: explicitly permitted/limited operational scope (e.g. "passive reconnaissance only", "only target a certain domain").
- A constraint is neither a goal nor an attack step: it defines operational boundaries.
- **Constraints must be [self-contained, with explicit concrete targets]**: replace **references** such as "current target/current port/current IP/current domain/this site" with **concrete values** from the task goal/description. Constraints are injected separately into execution prompts, where such references become ambiguous without context.
  Example: for https://abc.example.net, write "only test abc.example.net", not "only test the current target"; write "only test target port 443; do not scan other ports", not "only test the current port". If the original text says "current target" but the target address is known, insert that address.
- **Register only constraints [explicitly stated or emphasized] in the goal/description; never invent them**. Use deny when unsure of the type (more conservative).
- If the goal/description contains no operational constraints, **do not** call set_constraints.
After registering constraints (if any), decompose goals as described below.

**Goal = a final deliverable/verifiable outcome**

**These are not goals (do not list them as subgoals)**:
- Information gathering, reconnaissance, endpoint scanning
- Vulnerability analysis and validation processes
- Attack steps and exploitation methods
- Result verification steps

**Decomposition principles**:
- If the user describes only one final goal, output one
- If there are multiple **independent** final deliverables, list them separately
- Set vulnclass when a goal maps to a clear vulnerability class; leave it empty for information gathering/business logic goals
- Never invent goals the user did not mention

Call set_goals to submit the results.`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**Additional responsibility: register the test asset scope**
Besides decomposing goals, identify the **explicitly specified test asset scope** in the "task goal / task description" and register it with add_task_scope (the authorization boundary for this task and the denominator for asset test coverage). **Minimum scope principle: register only the exact target explicitly named by the user; never expand it on your own.**
- If the target is a URL or an address containing a hostname (e.g. https://xxx.example.com/path, app.example.com), use its **full hostname**, kind=subdomain, value=the full hostname.
  Example: target https://a1b2c3.lab.example.net/path → kind=subdomain, value=a1b2c3.lab.example.net (**not** example.net).
  **Never** reduce a hostname with a subdomain to its root domain: registering all of example.com when given xxx.example.com expands beyond the user target and violates the minimum scope principle.
- Use kind=root_domain, value=example.com only when the user supplies a **bare root domain with no subdomain** (e.g. example.com), or explicitly says "entire site / all subdomains / entire domain".
- For a plain IP or network range, use kind=ip / cidr, value=IP or CIDR.
- **Do not** register company scope (company): the task has just been created and the company usually does not exist in the asset system yet, so registration would fail. Leave company-level scope to the later plan phase.
Other rules:
- Register only scope **explicitly stated in the goal/description**; never invent or infer unmentioned domains/IPs.
- In reason, briefly identify the source statement for auditing.
- If the goal/description contains no explicit asset scope, **do not** call add_task_scope.
First register scope with add_task_scope (if any), then call set_goals to submit goals.`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc is the task's free-text description (背景：靶标范围/flag 数量/交战说明等).
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// 目标拆解是一次性调用：不挂 transcript store，所以 agentcore 不会往 ctx 上挂
	// session id（它只在有 writer 时才挂，见 agentcore.Prompt）。而按 session-id 头
	// 做提示缓存/粘性路由的网关（opencode zen 缺 x-opencode-session 直接 400
	// MissingSessionID）读的就是 ctx 上这个值——不补就是「对话正常、拆解 400」。
	// 显式挂一个稳定 id：同一探索的拆解请求共享它（利于命中缓存），且命名与
	// planner/worker 不冲突，能被 llmrec.parseSession 正确归因。
	if ts != nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	// set_constraints 始终可用(不依赖 asset store):正文已含「先抽操作约束再拆目标」这步
	// (可在 agent 编辑页改措辞),这里只需接上工具。
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += goalsScopeTail
	}
	userMsg := "Task goal:\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\nTask description (background information that may include target scope/flag count/rules of engagement; for reference only, do not invent unmentioned content):\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// 3 步(抽约束 → 登记范围 → 拆目标)各需一次工具调用,给足回合避免收尾前漏调 set_goals。
		MaxTurns:     8,
		NonStreaming: nonStreaming, // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    maxTokens,    // 0 = 不发上限,由服务端默认值决定
	}, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _ := ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n := range nodes {
		var p struct {
			Text      string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text) != "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
