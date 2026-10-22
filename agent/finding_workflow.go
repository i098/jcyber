package agent

import (
	"encoding/json"
	"fmt"

	"pta/db"
	actool "normasdk/tool"
)

const findingIDGuidance = "\n\n**Finding ID convention**: finding_id is the standalone finding record ID; finding_node_id is the exploration node ID. The id returned by list_findings / list_task_findings / node_detail / get_task_node_detail remains the exploration node ID; read the standalone ID from finding_id in the same response. get_finding_traffic / bind_finding_traffic use the standalone finding_id. The legacy finding_id parameter of update_finding_report still takes finding_node_id. Do not use the number on the first line of report_finding with evidence tools, and do not guess other numbers after an ID error."

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled != nil && FindingTrafficBindingEnabled()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled() {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = "\nBy default, the report Agent verifies and binds traffic before writing the report. Preserve verification commands, key output, existing real traffic IDs, and their purposes in evidence so the report Agent can verify them against execution records; no extra packet lookup is needed just for binding. Explicit immediate binding is also supported: traffic_refs or evidence_hint_id can supply verified references, with the latter reading structured references from the specified hint in this task; any invalid reference causes the entire submission to fail. TCP/no-packet cases do not need these optional parameters. The returned finding_id and finding_node_id identify the standalone record and exploration node, respectively."
		case "add_hint", "add_task_hint":
			note = "\nWhen handing off a confirmed vulnerability, preserve verified traffic IDs, roles, notes, and order in the corresponding hint's traffic_refs (at the top level for a single hint, or in the corresponding hints element for a batch), and explain in text which specific vulnerability it proves. Do not hand off only text while discarding existing traffic references. Unverified candidates must not be passed as evidence."
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n**Traffic evidence handoff (optional)**: By default, the report Agent performs automatic binding after the finding is stored and before writing the report. Preserve verification commands, key output, existing real traffic IDs, and their purposes in evidence, with intent_id for tasks, so the report Agent can trace them; no extra packet lookup is needed just for binding. Auto / Planner must not discard the executor's existing references when reporting on its behalf. add_hint / add_task_hint can hand off traffic_refs; explicit immediate binding through report_finding's traffic_refs / evidence_hint_id is still supported. Register TCP or no-packet findings normally; never guess IDs or repeat probes solely to obtain packets."
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\nWhen a platform chat has no task context, do not call report_finding directly; hand off to the corresponding existing task through add_task_hint, let its task Agent register the finding, and verify the result with list_task_findings."
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\nBefore declaring the goal complete, finish reporting or handing off the evidence already obtained in this run. Do not end the task or cancel Workers merely because a textual finding was registered while evidence handoff remains incomplete; no-packet cases do not require waiting or forced capture."
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**Automatic traffic association before reporting (enabled)**: Verify and bind traffic for the finding that triggered this run, then write its report. First obtain explicit finding_id and finding_node_id from the report_finding JSON response or get_task_node_detail / list_task_findings. Read the finding details, the corresponding intent's execution records, and the existing evidence list, preferring real IDs handed off by the reporter. If this verification used HTTP and traffic tools are available, filter candidates with traffic_search, then use traffic_get to verify each request/response actually supports this vulnerability; domain and time only filter candidates and do not prove association. Bind confirmed evidence in reproduction order using bind_finding_traffic(finding_id, traffic_refs), selecting baseline / proof / verification / supporting and explaining its purpose. Operate only on this finding; do not create duplicate findings or probe the target again. After binding succeeds, call get_finding_traffic again to obtain the latest version, read the required bodies, and pass the version actually read as evidence_version to update_finding_report (whose finding_id parameter still uses finding_node_id). Do not append existing bindings again. For TCP, uncaptured traffic, unavailable tools, or no exact match, skip automatic binding, write the report normally from text/command evidence, and explain why; never guess to fill missing traffic. Do not claim binding succeeded when it failed; preserve existing evidence and explain the failure in the report."
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "Optional: verified traffic references corresponding to specific vulnerabilities in this hint, preserving order; after handoff, report_finding can pass evidence_hint_id to carry these references.", "items": obj(map[string]any{"traffic_id": str("Real traffic ID"), "role": str("baseline / proof / verification / supporting"), "note": str("What conclusion this traffic supports")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d must be a hint node in this task (inherited hints cannot be used directly for binding)", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
