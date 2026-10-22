package agent

import (
	"context"

	"pta/db"
)

// FindingRecorder is injected by the host; agents never synthesize or copy
// evidence bodies themselves. Its implementation owns the atomic write.
type FindingRecorder interface {
	Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error)
}

// Tool-use guidance is appended without replacing the user's editable prompt.
// It does not require capture or claim that unavailable traffic tools exist.
const findingTrafficGuidance = "\n\n**Vulnerability traffic evidence (optional)**: When reporting a vulnerability with report_finding, if you have inspected HTTP requests/responses and confirmed they support the finding, use traffic_refs to bind real IDs in reproduction order. Domains and timestamps only filter candidates; they do not establish a relationship. For non-HTTP vulnerabilities such as TCP, uncaptured traffic, or no exact match, omit traffic_refs or pass []; retain other verifiable evidence such as command output and logs in evidence, preferably explaining why no traffic was bound. Do not guess IDs or repeat probes solely to collect packets."

func (t *ToolSet) SetFindingRecorder(r FindingRecorder)   { t.findingRecorder = r }
func (w *Worker) SetFindingRecorder(r FindingRecorder)    { w.findingRecorder = r }
func (p *Planner) SetFindingRecorder(r FindingRecorder)   { p.findingRecorder = r }
func (m *MainAgent) SetFindingRecorder(r FindingRecorder) { m.findingRecorder = r }
