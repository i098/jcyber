package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "User paused the task",
		"The user paused the task through the task control endpoint (POST /api/tasks/{id}/control, action=pause). This Planner/Worker run was explicitly canceled; running intents return to frontier(open) and are reclaimed and executed from the start when the task resumes")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "Orchestration Agent paused the task",
		"The orchestration Agent called pause_task to pause this task. This Planner/Worker run was explicitly canceled; running intents return to frontier(open) and run again when resumed")
	AbortTaskDeleted = cause("task_deleted", "Task deleted",
		"The task is being deleted (DELETE /api/tasks/{id}); the deletion barrier canceled its running Planner, Worker, and main Agent. Results from this run will no longer be used")
	AbortPausedOnReload = cause("paused_on_reload", "Backend restored the task's paused state",
		"The backend restored the task's paused state from the database at startup. This run was canceled; normally no Agent is running during restoration")
	AbortGoalMet = cause("goal_met", "Planner determined the task goal was met",
		"The Planner determined the task goal was met, set the task to done, and then canceled remaining Workers; these intents are marked stopped, not failed")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "Task timeout drain period expired",
		"After the task reached timeout, running Workers were allowed to finish gracefully, but the 90-second drain grace period expired, triggering hard cancellation; intents are marked exhausted, and facts and assets already written during draining are retained")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "Planner terminated this intent",
		"The Planner called kill_work to terminate this intent, usually because it went off track or had no further value; the intent is marked stopped and will not be reclaimed automatically")
	AbortWorkPausedByUser = cause("work_paused_by_user", "User paused this Worker intent",
		"The user paused the running Worker. This call was canceled and the intent became paused; all recorded intents, facts, vulnerabilities, and activity records are retained, and execution restarts from the beginning when resumed")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "User deleted this Worker intent",
		"The user deleted the running Worker. This call was canceled; after the Worker exits the write section, the server applies the selected deletion mode: soft deletion only marks it deleted and retains all outputs, while hard deletion cascades to this intent and downstream nodes supported exclusively by it")
	AbortWorkFinished = cause("work_finished", "Worker finished; context released",
		"The Worker finished normally, and the engine released its context resources in detachWork. This is not a run interruption; if shown as an interruption message, cancellation raced with cleanup")
	AbortPausedRaceGuard = cause("paused_race_guard", "New run rejected while task is paused",
		"While the task is paused, the engine refuses to issue new execution contexts to prevent a race between claim and pause from starting Workers; claimed intents return to frontier")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "User stopped this chat turn",
		"The user clicked Stop to terminate this main Agent or session Agent run. Existing activity records are retained, and another message can be sent")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "Task paused and main Agent chat stopped",
		"When the user paused the task, the running main Agent chat was also canceled. Existing activity records are retained; this message will not replay automatically when the task resumes")
	AbortChatTurnFinished = cause("chat_turn_finished", "Chat turn finished; context released",
		"This chat turn finished normally, and the server is releasing its context resources. This is not a run interruption; if shown as an interruption message, cancellation raced with cleanup")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "Backend process is shutting down",
		"The backend received SIGINT or SIGTERM and is restarting, updating, or shutting down. All running Agents are canceled; remaining running intents are reset to open and executed again after restart")
	AbortRunHardTimeout = cause("run_hard_timeout", "Run hard-timeout safeguard triggered",
		"This run exceeded its soft wall-clock budget and additional grace period, indicating that a model request or tool has not returned for a long time, preventing normal cleanup at the turn boundary. Check the last outstanding tool call before the interruption")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "Upstream context reached its deadline",
			"The upstream context reached its deadline, but its creator did not attach a named cause using WithTimeoutCause: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "Cancellation has no named cause",
			"The upstream context was canceled without a named cause attached using context.WithCancelCause; register the cause in agent/cancelcause.go and connect it to this cancellation point", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
