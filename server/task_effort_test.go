package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"normasdk/llm"
	"pta/agent"
	"pta/db"
)

func effortWireServer(t *testing.T) (*httptest.Server, <-chan map[string]any) {
	t.Helper()
	requests := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var data map[string]any
		if json.Unmarshal(body, &data) == nil {
			requests <- data
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	return srv, requests
}

func effortWireRequest(t *testing.T, prov llm.Provider, requests <-chan map[string]any) map[string]any {
	t.Helper()
	_, _, _, _ = prov.Complete(context.Background(), llm.CompletionRequest{})
	select {
	case body := <-requests:
		return body
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not send a request")
		return nil
	}
}

func assertEffortWire(t *testing.T, body map[string]any, model, effort string) {
	t.Helper()
	if body["model"] != model {
		t.Fatalf("request model = %v, want %s", body["model"], model)
	}
	reasoning, present := body["reasoning"]
	if effort == ThinkingEffortAuto {
		if present {
			t.Fatalf("auto request sent reasoning: %v", reasoning)
		}
		return
	}
	fields, ok := reasoning.(map[string]any)
	if !ok || fields["effort"] != effort {
		t.Fatalf("request reasoning = %v, want effort %s", reasoning, effort)
	}
}

func TestResolvedEffortConfig(t *testing.T) {
	base := agent.Config{ThinkingType: "enabled", ReasoningEffort: "high", Model: "chosen-model"}
	auto := resolvedEffortConfig(base, ThinkingEffortAuto)
	if auto.ThinkingType != "" || auto.ReasoningEffort != "" || auto.Model != base.Model {
		t.Fatalf("auto config = %+v, want provider-default thinking on chosen model", auto)
	}
	max := resolvedEffortConfig(base, ThinkingEffortMax)
	if max != base {
		t.Fatalf("max config = %+v, want selected profile config %+v", max, base)
	}
	disabledBase := agent.Config{ThinkingType: "disabled", ReasoningEffort: "high"}
	if disabled := resolvedEffortConfig(disabledBase, ThinkingEffortMax); disabled != disabledBase {
		t.Fatalf("disabled profile max config = %+v, want selected profile config %+v", disabled, disabledBase)
	}
	if legacy := resolvedEffortConfig(base, ""); legacy != base {
		t.Fatalf("legacy config = %+v, want unchanged %+v", legacy, base)
	}
}

func TestResolvedEffortWire(t *testing.T) {
	srv, requests := effortWireServer(t)
	defer srv.Close()
	base := agent.Config{Format: llm.FormatOpenAIResponses, BaseURL: srv.URL, APIKey: "test-key", Model: "gpt-5.1", ThinkingType: "enabled", ReasoningEffort: "high"}
	for _, effort := range []string{ThinkingEffortAuto, ThinkingEffortMax} {
		cfg := resolvedEffortConfig(base, effort)
		prov, err := cfg.NewProvider()
		if err != nil {
			t.Fatal(err)
		}
		wireEffort := effort
		if effort == ThinkingEffortMax {
			wireEffort = base.ReasoningEffort
		}
		assertEffortWire(t, effortWireRequest(t, prov, requests), base.Model, wireEffort)
	}
}

func TestTaskEffortRoutingAcrossBindings(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	srv, requests := effortWireServer(t)
	defer srv.Close()
	stamp := time.Now().UnixNano()
	boundID, err := m.pg.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("effort-bound-%d", stamp), Format: "openai-responses", BaseURL: srv.URL, Model: "bound-model", APIKey: "test-key", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	chainID, err := m.pg.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("effort-chain-%d", stamp), Format: "openai-responses", BaseURL: srv.URL, Model: "chain-model", APIKey: "test-key", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	prior, err := m.pg.GetAgentByKey("worker")
	if err != nil {
		t.Fatal(err)
	}
	var priorID *int64
	if prior != nil {
		priorID = prior.LLMProfileID
	}
	var taskIDs []string
	defer func() {
		_ = m.pg.SetAgentLLMProfile("worker", priorID)
		for _, id := range taskIDs {
			_, _ = m.DeleteTask(id, DeleteTaskOptions{})
		}
		_ = m.pg.DeleteProfile(boundID)
		_ = m.pg.DeleteProfile(chainID)
	}()
	autoTask, err := m.CreateTaskWithOptions("auto effort", "use binding", db.TaskCreateOptions{LLMProfileIDs: []int64{chainID}, ThinkingEffort: ThinkingEffortAuto})
	if err != nil {
		t.Fatal(err)
	}
	taskIDs = append(taskIDs, autoTask.ID)
	if err := m.pg.SetAgentLLMProfile("worker", &boundID); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m, llmHealth: newLLMHealthRegistry(m.pg)}
	selection, err := (&taskLLMRuntime{s: s, taskID: autoTask.ID, agentKey: "worker"}).current()
	if err != nil {
		t.Fatal(err)
	}
	assertEffortWire(t, effortWireRequest(t, selection.provider, requests), "bound-model", ThinkingEffortAuto)
	if err := m.pg.SetAgentLLMProfile("worker", nil); err != nil {
		t.Fatal(err)
	}
	maxTask, err := m.CreateTaskWithOptions("max effort", "use chain", db.TaskCreateOptions{LLMProfileIDs: []int64{chainID}, ThinkingEffort: ThinkingEffortMax})
	if err != nil {
		t.Fatal(err)
	}
	taskIDs = append(taskIDs, maxTask.ID)
	selection, err = (&taskLLMRuntime{s: s, taskID: maxTask.ID, agentKey: "worker"}).current()
	if err != nil {
		t.Fatal(err)
	}
	assertEffortWire(t, effortWireRequest(t, selection.provider, requests), "chain-model", "high")
}
