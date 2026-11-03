package server

import (
	"context"
	"encoding/json"
	"errors"
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

func TestMaxEffortUsesGlobalProvider(t *testing.T) {
	direct := &scriptedLLMProvider{}
	s := &Server{llmOn: true, llmProv: direct}
	provider, _, ok := s.globalProviderForEffort(ThinkingEffortMax)
	if !ok {
		t.Fatal("max effort did not resolve the global provider")
	}
	_, _, _, err := provider.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil || direct.calls != 1 {
		t.Fatalf("global provider calls = %d, error = %v; want one call without error", direct.calls, err)
	}
}

func TestGlobalAutoEffortUsesAppliedConfig(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	srv, requests := effortWireServer(t)
	defer srv.Close()
	previous, err := m.pg.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	profileID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: fmt.Sprintf("effort-global-%d", time.Now().UnixNano()), Format: "openai-responses",
		BaseURL: srv.URL, Model: "gpt-5.1", APIKey: "test-key", ReasoningEffort: "high",
		Streaming: false, MaxTokens: 4096, RatePerMinute: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if previous != nil {
			_ = m.pg.SetActiveProfile(previous.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, profileID)
		}
		_ = m.pg.DeleteProfile(profileID)
	}()
	if err := m.pg.SetActiveProfile(profileID); err != nil {
		t.Fatal(err)
	}
	poolEnabled := m.LLMPoolEnabled()
	if err := m.SetLLMPoolEnabled(false); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.SetLLMPoolEnabled(poolEnabled) }()

	applied := agent.ConfigFrom("openai-responses", "gpt-5.1", srv.URL, "test-key", "")
	applied.ReasoningEffort = "high"
	applied.RatePerMinute = 1
	s := &Server{m: m, llmHealth: newLLMHealthRegistry(m.pg)}
	if err := s.applyLLM(applied); err != nil {
		t.Fatal(err)
	}
	maxProvider, _, ok := s.globalProviderForEffort(ThinkingEffortMax)
	if !ok {
		t.Fatal("max effort did not resolve applied provider")
	}
	autoProvider, autoCfg, ok := s.globalProviderForEffort(ThinkingEffortAuto)
	if !ok || autoCfg.Stream != applied.Stream || autoCfg.MaxTokens != applied.MaxTokens {
		t.Fatalf("auto config = %+v, available = %v; want applied streaming and output limit", autoCfg, ok)
	}
	assertEffortWire(t, effortWireRequest(t, maxProvider, requests), applied.Model, applied.ReasoningEffort)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _, _, err = autoProvider.Complete(ctx, llm.CompletionRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("auto provider did not share max provider's rate limit: %v", err)
	}
	select {
	case request := <-requests:
		t.Fatalf("auto provider sent a second request before rate limit: %v", request)
	default:
	}
}

func TestGlobalAutoEffortDoesNotAttributeOldConfigToNewActiveProfile(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	requests := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var data map[string]any
		if json.Unmarshal(body, &data) == nil {
			requests <- data
		}
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	previous, err := m.pg.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UnixNano()
	oldID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: fmt.Sprintf("effort-old-active-%d", stamp), Format: "openai-responses",
		BaseURL: srv.URL, Model: "old-model", APIKey: "test-key", ReasoningEffort: "high", RatePerMinute: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	newID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: fmt.Sprintf("effort-new-active-%d", stamp), Format: "openai-responses",
		BaseURL: srv.URL, Model: "new-model", APIKey: "test-key", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if previous != nil {
			_ = m.pg.SetActiveProfile(previous.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, newID)
		}
		_ = m.pg.DeleteProfile(oldID)
		_ = m.pg.DeleteProfile(newID)
	}()
	if err := m.pg.SetActiveProfile(oldID); err != nil {
		t.Fatal(err)
	}
	poolEnabled := m.LLMPoolEnabled()
	if err := m.SetLLMPoolEnabled(true); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.SetLLMPoolEnabled(poolEnabled) }()
	s := &Server{m: m, llmHealth: newLLMHealthRegistry(m.pg)}
	cfg, ok := s.loadLLMConfig()
	if !ok {
		t.Fatal("old active profile unavailable")
	}
	if err := s.applyLLM(cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetActiveProfile(newID); err != nil {
		t.Fatal(err)
	}
	s.resetGlobalAutoProvider()
	s.invalidateProfileChatAgents()
	s.invalidateTaskAgents()
	provider, autoCfg, ok := s.globalProviderForEffort(ThinkingEffortAuto)
	if !ok || autoCfg.Model != "old-model" {
		t.Fatalf("auto config = %+v, available = %v; want old applied model", autoCfg, ok)
	}
	assertEffortWire(t, effortWireRequest(t, provider, requests), "old-model", ThinkingEffortAuto)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _, _, err = s.llmDirect.Complete(ctx, llm.CompletionRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("applied profile's limiter did not include auto request: %v", err)
	}
	select {
	case request := <-requests:
		t.Fatalf("applied profile sent a second request before rate limit: %v", request)
	default:
	}
	if state, recorded := s.llmHealth.Snapshot()[newID]; recorded {
		t.Fatalf("new active profile got old provider's failure: %+v", state)
	}
}

func TestApplyLLMInvalidatesStaleGlobalAutoProvider(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	srv, requests := effortWireServer(t)
	defer srv.Close()
	previous, err := m.pg.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	profile := &db.LLMProfile{
		Name: fmt.Sprintf("effort-update-%d", time.Now().UnixNano()), Format: "openai-responses",
		BaseURL: srv.URL, Model: "old-model", APIKey: "test-key", ReasoningEffort: "high", Streaming: true,
	}
	id, err := m.pg.SaveProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if previous != nil {
			_ = m.pg.SetActiveProfile(previous.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, id)
		}
		_ = m.pg.DeleteProfile(id)
	}()
	if err := m.pg.SetActiveProfile(id); err != nil {
		t.Fatal(err)
	}
	poolEnabled := m.LLMPoolEnabled()
	if err := m.SetLLMPoolEnabled(false); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.SetLLMPoolEnabled(poolEnabled) }()
	s := &Server{m: m, llmHealth: newLLMHealthRegistry(m.pg)}
	oldConfig, ok := s.loadLLMConfig()
	if !ok {
		t.Fatal("old profile unavailable")
	}
	if err := s.applyLLM(oldConfig); err != nil {
		t.Fatal(err)
	}
	profile.ID, profile.Model = id, "new-model"
	if _, err := m.pg.SaveProfile(profile); err != nil {
		t.Fatal(err)
	}
	s.invalidateProfileAgents()
	_, staleConfig, ok := s.globalProviderForEffort(ThinkingEffortAuto)
	if !ok || staleConfig.Model != "old-model" {
		t.Fatalf("pre-reapply auto config = %+v, available = %v; want old applied model", staleConfig, ok)
	}
	newConfig, ok := s.loadLLMConfig()
	if !ok {
		t.Fatal("new profile unavailable")
	}
	if err := s.applyLLM(newConfig); err != nil {
		t.Fatal(err)
	}
	s.provCacheMu.Lock()
	oldGeneration := s.provCacheGen
	s.provCacheMu.Unlock()
	s.resetGlobalAutoProvider()
	if _, _, ok := s.environmentProviderForEffort(oldConfig, s.llmProf, ThinkingEffortAuto, &oldGeneration); ok {
		t.Fatal("stale auto provider built after cache generation changed")
	}
	provider, cfg, ok := s.globalProviderForEffort(ThinkingEffortAuto)
	if !ok || cfg.Model != "new-model" {
		t.Fatalf("post-reapply auto config = %+v, available = %v; want new applied model", cfg, ok)
	}
	assertEffortWire(t, effortWireRequest(t, provider, requests), "new-model", ThinkingEffortAuto)
}

func TestSideAutoEffortUsesCapturedProviderWhenPoolFailsOver(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	activeServer, activeRequests := effortWireServer(t)
	defer activeServer.Close()
	fallbackServer, fallbackRequests := effortWireServer(t)
	defer fallbackServer.Close()
	previous, err := m.pg.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UnixNano()
	activeID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: fmt.Sprintf("side-auto-active-%d", stamp), Format: "openai-responses",
		BaseURL: activeServer.URL, Model: "active-model", APIKey: "test-key", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	fallbackID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: fmt.Sprintf("side-auto-fallback-%d", stamp), Format: "openai-responses",
		BaseURL: fallbackServer.URL, Model: "fallback-model", APIKey: "test-key", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if previous != nil {
			_ = m.pg.SetActiveProfile(previous.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, activeID)
		}
		_ = m.pg.DeleteProfile(activeID)
		_ = m.pg.DeleteProfile(fallbackID)
	}()
	if err := m.pg.SetActiveProfile(activeID); err != nil {
		t.Fatal(err)
	}
	poolEnabled := m.LLMPoolEnabled()
	if err := m.SetLLMPoolEnabled(true); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.SetLLMPoolEnabled(poolEnabled) }()
	s := &Server{m: m, llmHealth: newLLMHealthRegistry(m.pg)}
	cfg, ok := s.loadLLMConfig()
	if !ok {
		t.Fatal("active profile unavailable")
	}
	if err := s.applyLLM(cfg); err != nil {
		t.Fatal(err)
	}
	model := sideModel(resolvedEffortConfig(cfg, ThinkingEffortAuto), 0, "active")
	s.llmHealth.Trip(activeID, "quota exhausted", true)
	provider, err := s.sideProvider(model)
	if err != nil {
		t.Fatal(err)
	}
	assertEffortWire(t, effortWireRequest(t, provider, activeRequests), cfg.Model, ThinkingEffortAuto)
	select {
	case body := <-fallbackRequests:
		t.Fatalf("side question used fallback profile: %v", body)
	default:
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
	defaultProvider, _, ok := s.providerForProfile(chainID)
	if !ok || selection.provider != defaultProvider {
		t.Fatal("max task did not reuse the selected profile provider")
	}
	assertEffortWire(t, effortWireRequest(t, selection.provider, requests), "chain-model", "high")
}
