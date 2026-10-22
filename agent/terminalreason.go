package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"normasdk/harness"
)

// runTrace retains the latest tool call so an interrupted run can identify the
// operation that was still in flight.
type runTrace struct {
	startedAt time.Time
	id        string
	name      string
	input     string
	at        time.Time
	pending   bool
}

func (t *runTrace) start(id, name, input string) {
	t.id, t.name, t.input, t.at, t.pending = id, name, input, time.Now(), true
}

func (t *runTrace) done(id string) {
	if id == t.id {
		t.pending = false
	}
}

var reasonHint = map[harness.TerminalReason]string{
	harness.ReasonCompleted:         "The model finished this round normally but left no text summary; use this round's tool call records as the source of truth for facts and assets",
	harness.ReasonMaxTurns:          "Reached the turn limit (MaxTurns): the SDK finalized and wrote back facts and assets; the intent will be marked exhausted so the planner can continue in another direction, not treated as a failure",
	harness.ReasonTimeout:           "Reached the wall-clock budget for this run (MaxDuration): active tools are interrupted and finalization begins immediately, writing back identified facts and assets; the intent will be marked exhausted",
	harness.ReasonModelError:        "Model or API call failed (network, authentication, rate limit, provider 5xx, etc.); after retries are exhausted, the intent is marked blocked -- a transport failure means this intent was barely explored; inspect its execution trace (get_worker_trace) before deciding whether to redispatch or change approach",
	harness.ReasonBlockingLimit:     "Context length reached the hard limit and the request was intercepted before being sent; narrow the intent scope or compress tool output",
	harness.ReasonPromptTooLong:     "The prompt is too long and context compression retries are exhausted; execution cannot continue",
	harness.ReasonImageError:        "The current model does not support this round's multimodal content; switch to a vision-capable model or avoid tool outputs containing images",
	harness.ReasonStopHookPrevented: "The Stop hook prevented this round from ending, and execution could not continue; check whether the task Guard rules are too strict",
	harness.ReasonHookStopped:       "A tool or hook stopped execution, for example due to an out-of-scope target or disabled command; check the interception explanation in the last tool_result",
	harness.ReasonAbortedStreaming:  "The run was canceled while streaming model output",
	harness.ReasonAbortedTools:      "The run was canceled during tool execution",
}

// terminalText renders a terminal event with no final text into a compact summary
// and a Markdown detail block.
func terminalText(ctx context.Context, term *harness.Terminal, tr *runTrace) (string, string) {
	reason := term.Reason
	aborted := reason == harness.ReasonAbortedStreaming || reason == harness.ReasonAbortedTools
	// Prompt may return ctx.Err directly without a terminal event. Preserve the
	// cancellation cause instead of falling back to an empty/unknown terminal reason.
	if reason == "" && ctx.Err() != nil {
		aborted = true
	}

	var sum string
	if aborted {
		_, short, _, ok := AbortReason(ctx)
		if !ok {
			short = "Cancellation reason unavailable"
		}
		stage := "execution"
		switch reason {
		case harness.ReasonAbortedStreaming:
			stage = "model output"
		case harness.ReasonAbortedTools:
			stage = "tool execution"
		}
		sum = "(Run interrupted: " + short + "; stopped during " + stage + progressSuffix(term, tr) + ", incomplete)"
	} else if reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout {
		sum = "(Run budget limit reached (" + string(reason) + "); finalized and wrote back facts" + progressSuffix(term, tr) + "; no text summary this time)"
	} else {
		hint := terminalReasonHint(reason)
		sum = "(No text summary, terminal state " + terminalReasonLabel(reason) + ": " + firstLine(hint, 80) + ")"
	}

	var b strings.Builder
	b.WriteString(sum)
	b.WriteString("\n\n")
	displayReason := terminalReasonLabel(reason)
	fmt.Fprintf(&b, "- **Terminal state**: `%s` - %s\n", displayReason, terminalReasonHint(reason))
	if aborted {
		code, _, why, ok := AbortReason(ctx)
		if ok {
			fmt.Fprintf(&b, "- **Interruption reason** (`%s`): %s\n", code, why)
		} else {
			b.WriteString("- **Interruption reason**: Unavailable; the caller may not have attached a named reason via context.WithCancelCause\n")
		}
	}
	if term.Err != nil {
		fmt.Fprintf(&b, "- **Underlying error**: `%v`\n", term.Err)
	}
	if aborted && strings.TrimSpace(term.Text) != "" {
		b.WriteString("- **Partial output generated before cancellation**:\n\n")
		b.WriteString(term.Text)
		b.WriteString("\n\n")
	}
	if term.Turns > 0 {
		fmt.Fprintf(&b, "- **Executed**: %d model turns\n", term.Turns)
	}
	if !tr.startedAt.IsZero() {
		fmt.Fprintf(&b, "- **Run duration**: %s\n", roundDur(time.Since(tr.startedAt)))
	}
	if u := term.Usage; u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, "- **Total tokens**: input %d / output %d / cache read %d / cache write %d\n",
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	}
	if tr.name == "" {
		b.WriteString("- **Tool calls**: This run ended before any tool calls were issued\n")
	} else if tr.pending {
		fmt.Fprintf(&b, "- **Tool running at interruption**: `%s` (running for %s, **no result returned**)\n\n  ```json\n  %s\n  ```\n",
			tr.name, roundDur(time.Since(tr.at)), firstLine(tr.input, 300))
	} else {
		fmt.Fprintf(&b, "- **Last tool before interruption**: `%s` (returned normally)\n", tr.name)
	}
	return sum, b.String()
}

func terminalReasonLabel(reason harness.TerminalReason) string {
	if reason == "" {
		return "context_canceled"
	}
	return string(reason)
}

func terminalReasonHint(reason harness.TerminalReason) string {
	if hint := reasonHint[reason]; hint != "" {
		return hint
	}
	if reason == "" {
		return "The run context was canceled, but the underlying layer did not produce a Terminal event"
	}
	return "Unknown terminal state; the harness may have added a TerminalReason; update reasonHint"
}

func progressSuffix(term *harness.Terminal, tr *runTrace) string {
	var parts []string
	if term.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d turns", term.Turns))
	}
	if !tr.startedAt.IsZero() {
		parts = append(parts, roundDur(time.Since(tr.startedAt)))
	}
	if len(parts) == 0 {
		return ""
	}
	return ", ran for " + strings.Join(parts, " / ")
}

func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	case d < time.Hour:
		return d.Round(time.Second).String()
	default:
		return d.Round(time.Minute).String()
	}
}
