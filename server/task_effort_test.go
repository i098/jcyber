package server

import (
	"bytes"
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
	"pta/llmpool"
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

func TestAutoProfileEffortUsesAppliedCache(t *testing.T) {
	srv, requests := effortWireServer(t)
	defer srv.Close()
	cfg := agent.ConfigFrom("openai-responses", "applied-model", srv.URL, "test-key", "")
	cfg.ThinkingType, cfg.ReasoningEffort, cfg.RatePerMinute = "enabled", "high", 1
	s := &Server{m: &Manager{}}
	maxProvider, _, ok := s.providerForEffortUnlocked(42, "", &cfg, "applied")
	if !ok {
		t.Fatal("applied profile provider unavailable")
	}
	assertEffortWire(t, effortWireRequest(t, maxProvider, requests), cfg.Model, cfg.ReasoningEffort)
	autoProvider, autoCfg, ok := s.providerForProfileEffort(42, ThinkingEffortAuto)
	if !ok || autoCfg.Model != cfg.Model || autoCfg.ThinkingType != "" || autoCfg.ReasoningEffort != "" {
		t.Fatalf("auto profile config = %+v, available = %v; want applied model with provider-default effort", autoCfg, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _, _, err := autoProvider.Complete(ctx, llm.CompletionRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("auto effort bypassed applied profile limiter: %v", err)
	}
	select {
	case request := <-requests:
		t.Fatalf("auto effort sent a second request before rate limit: %v", request)
	default:
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

func TestEnvironmentEffortKeepsLimiterAfterInactiveProfileSave(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	previous, err := m.pg.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	if previous != nil {
		if _, err := m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, previous.ID); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = m.pg.SetActiveProfile(previous.ID) }()
	}
	srv, requests := effortWireServer(t)
	defer srv.Close()
	cfg := agent.ConfigFrom("openai-responses", "environment-model", srv.URL, "test-key", "")
	cfg.ThinkingType, cfg.ReasoningEffort, cfg.RatePerMinute = "enabled", "high", 1
	s := &Server{m: m, llmHealth: newLLMHealthRegistry(m.pg)}
	if err := s.applyLLM(cfg); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"name": fmt.Sprintf("inactive-%d", time.Now().UnixNano()), "format": "openai-responses",
		"model": "inactive-model", "base_url": srv.URL, "api_key": "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	s.pgSaveProfile(recorder, httptest.NewRequest(http.MethodPost, "/api/llm/profiles", bytes.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("save inactive profile: %d %s", recorder.Code, recorder.Body.String())
	}
	var saved struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.pg.DeleteProfile(saved.ID) }()
	maxProvider, _, ok := s.globalProviderForEffort(ThinkingEffortMax)
	if !ok {
		t.Fatal("max effort did not resolve environment provider")
	}
	assertEffortWire(t, effortWireRequest(t, maxProvider, requests), cfg.Model, cfg.ReasoningEffort)
	if err := s.rebuildCurrentLLM(); err != nil {
		t.Fatal(err)
	}
	autoProvider, _, ok := s.globalProviderForEffort(ThinkingEffortAuto)
	if !ok {
		t.Fatal("auto effort did not resolve environment provider")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _, _, err = autoProvider.Complete(ctx, llm.CompletionRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("auto effort did not share environment limiter: %v", err)
	}
	select {
	case request := <-requests:
		t.Fatalf("auto effort sent a second request before rate limit: %v", request)
	default:
	}
}

func TestUnchangedActiveProfileKeepsLimiterAfterOtherProfileSave(t *testing.T) {
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
	activeID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: fmt.Sprintf("active-unchanged-%d", time.Now().UnixNano()), Format: "openai-responses",
		BaseURL: srv.URL, Model: "active-model", APIKey: "test-key", RatePerMinute: 1,
		ThinkingType: "enabled", ReasoningEffort: "high", Streaming: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var savedID int64
	defer func() {
		if previous != nil {
			_ = m.pg.SetActiveProfile(previous.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, activeID)
		}
		if savedID != 0 {
			_ = m.pg.DeleteProfile(savedID)
		}
		_ = m.pg.DeleteProfile(activeID)
	}()
	if err := m.pg.SetActiveProfile(activeID); err != nil {
		t.Fatal(err)
	}
	poolEnabled := m.LLMPoolEnabled()
	if err := m.SetLLMPoolEnabled(false); err != nil {
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
	maxProvider, _, ok := s.globalProviderForEffort(ThinkingEffortMax)
	if !ok {
		t.Fatal("max effort did not resolve active provider")
	}
	assertEffortWire(t, effortWireRequest(t, maxProvider, requests), cfg.Model, cfg.ReasoningEffort)
	body, err := json.Marshal(map[string]any{
		"name": fmt.Sprintf("inactive-other-%d", time.Now().UnixNano()), "format": "openai-responses",
		"model": "other-model", "base_url": srv.URL, "api_key": "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	s.pgSaveProfile(recorder, httptest.NewRequest(http.MethodPost, "/api/llm/profiles", bytes.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("save other profile: %d %s", recorder.Code, recorder.Body.String())
	}
	var saved struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	savedID = saved.ID
	autoProvider, autoCfg, ok := s.globalProviderForEffort(ThinkingEffortAuto)
	if !ok || autoCfg.Model != cfg.Model {
		t.Fatalf("auto config = %+v, available = %v", autoCfg, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _, _, err = autoProvider.Complete(ctx, llm.CompletionRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("auto effort did not share active profile limiter: %v", err)
	}
	select {
	case request := <-requests:
		t.Fatalf("auto effort sent a second request before rate limit: %v", request)
	default:
	}
}

func TestInvalidActiveProfileSaveKeepsRunningLimiter(t *testing.T) {
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
		Name: fmt.Sprintf("invalid-save-%d", time.Now().UnixNano()), Format: "openai-responses",
		BaseURL: srv.URL, Model: "profile-model", APIKey: "test-key", RatePerMinute: 1, Priority: 10,
		ThinkingType: "enabled", ReasoningEffort: "high", Streaming: true,
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
	cfg, ok := s.loadLLMConfig()
	if !ok {
		t.Fatal("active profile unavailable")
	}
	if err := s.applyLLM(cfg); err != nil {
		t.Fatal(err)
	}
	maxProvider, _, ok := s.globalProviderForEffort(ThinkingEffortMax)
	if !ok {
		t.Fatal("max effort unavailable")
	}
	assertEffortWire(t, effortWireRequest(t, maxProvider, requests), cfg.Model, cfg.ReasoningEffort)
	checkAutoBlocked := func(stage string) {
		t.Helper()
		autoProvider, _, ok := s.globalProviderForEffort(ThinkingEffortAuto)
		if !ok {
			t.Fatalf("%s: auto effort unavailable", stage)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, _, _, err := autoProvider.Complete(ctx, llm.CompletionRequest{})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: auto effort bypassed running limiter: %v", stage, err)
		}
		select {
		case request := <-requests:
			t.Fatalf("%s: auto effort sent a second request before rate limit: %v", stage, request)
		default:
		}
	}
	profile.Name = "renamed-" + profile.Name
	update, err := json.Marshal(map[string]any{
		"id": id, "name": profile.Name, "format": profile.Format, "base_url": profile.BaseURL,
		"model": profile.Model, "proxy": "", "rate_per_minute": 1, "streaming": true, "priority": profile.Priority,
		"thinking_type": profile.ThinkingType, "reasoning_effort": profile.ReasoningEffort,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	s.pgSaveProfile(recorder, httptest.NewRequest(http.MethodPost, "/api/llm/profiles", bytes.NewReader(update)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("profile rename: %d %s", recorder.Code, recorder.Body.String())
	}
	checkAutoBlocked("renamed profile")
	draftName := "draft-" + profile.Name
	update, err = json.Marshal(map[string]any{
		"id": id, "name": draftName, "format": profile.Format, "base_url": profile.BaseURL,
		"model": "draft-model", "proxy": "://invalid", "rate_per_minute": 1, "streaming": true, "priority": 0,
		"thinking_type": profile.ThinkingType, "reasoning_effort": profile.ReasoningEffort,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	s.pgSaveProfile(recorder, httptest.NewRequest(http.MethodPost, "/api/llm/profiles", bytes.NewReader(update)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("invalid profile save: %d %s", recorder.Code, recorder.Body.String())
	}
	saved, err := m.pg.ProfileByID(id)
	if err != nil || saved == nil || saved.Proxy != "://invalid" || saved.Name != draftName || saved.Model != "draft-model" {
		t.Fatalf("draft proxy missing: profile = %+v, error = %v", saved, err)
	}
	s.reapplyActiveProfile()
	if s.llmCfg.Proxy != "" {
		t.Fatalf("invalid proxy replaced running config: %q", s.llmCfg.Proxy)
	}
	global, err := s.resolveTaskRoleLLM(&Task{}, "unbound-resolution-role")
	if err != nil || !global.Available || global.ProfileID == nil || *global.ProfileID != id || global.Name != profile.Name || global.Model != profile.Model {
		t.Fatalf("global resolution reported draft: %+v, error = %v", global, err)
	}
	chainTask := &Task{LLMProfileIDs: []int64{id}, ActiveLLMProfileID: &id}
	chain, err := s.resolveTaskRoleLLM(chainTask, "unbound-resolution-role")
	if err != nil || !chain.Available || chain.Name != profile.Name || chain.Model != profile.Model {
		t.Fatalf("task chain reported draft: %+v, error = %v", chain, err)
	}
	if err := s.rebuildCurrentLLM(); err != nil {
		t.Fatal(err)
	}
	global, err = s.resolveTaskRoleLLM(&Task{}, "unbound-resolution-role")
	if err != nil || global.Name != profile.Name || global.Model != profile.Model {
		t.Fatalf("settings rebuild reported draft: %+v, error = %v", global, err)
	}
	s.resetGlobalAutoProvider()
	queuedAuto, queuedCfg, ok := s.providerForProfileEffort(id, ThinkingEffortAuto)
	if !ok || queuedCfg.Model != profile.Model {
		t.Fatalf("queued auto resolved draft: config = %+v, available = %v", queuedCfg, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, _, _, err = queuedAuto.Complete(ctx, llm.CompletionRequest{})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued auto bypassed running limiter: %v", err)
	}
	select {
	case request := <-requests:
		t.Fatalf("queued auto sent a second request before rate limit: %v", request)
	default:
	}
	checkAutoBlocked("invalid draft")
	previousPolicy := m.pg.LLMRetryPolicy()
	defer func() { _ = m.pg.SetLLMRetryPolicy(previousPolicy) }()
	fallbackName := fmt.Sprintf("retry-fallback-%d", time.Now().UnixNano())
	fallbackID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: fallbackName, Format: "openai-responses",
		BaseURL: srv.URL, Model: "fallback-model", APIKey: "test-key", Streaming: true, Priority: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.pg.DeleteProfile(fallbackID) }()
	if _, _, ok := s.providerForProfileEffort(fallbackID, ""); !ok {
		t.Fatal("fallback profile unavailable")
	}
	policy, err := json.Marshal(db.LLMRetryPolicy{Connect: db.RetryRule{Attempts: 1}})
	if err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	s.pgSaveLLMRetryPolicy(recorder, httptest.NewRequest(http.MethodPost, "/api/llm/retry-policy", bytes.NewReader(policy)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("retry policy save: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, _, ok := s.providerForProfileEffort(id, ""); !ok {
		t.Fatal("running profile unavailable after retry policy save")
	}
	_, fallbackCfg, ok := s.providerForProfileEffort(fallbackID, "")
	if !ok || fallbackCfg.Retry.ConnectAttempts != 1 {
		t.Fatalf("fallback profile retry config = %+v, available = %v", fallbackCfg.Retry, ok)
	}
	checkAutoBlocked("retry policy")
	update, err = json.Marshal(map[string]any{
		"id": fallbackID, "name": "draft-" + fallbackName, "format": "openai-responses", "base_url": srv.URL,
		"model": "draft-fallback", "proxy": "://invalid", "streaming": true, "priority": 0, "pool_exclude": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	s.pgSaveProfile(recorder, httptest.NewRequest(http.MethodPost, "/api/llm/profiles", bytes.NewReader(update)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("invalid fallback save: %d %s", recorder.Code, recorder.Body.String())
	}
	otherID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: fmt.Sprintf("other-fallback-%d", time.Now().UnixNano()), Format: "openai-responses",
		BaseURL: srv.URL, Model: "other-model", APIKey: "test-key", Streaming: true, Priority: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.pg.DeleteProfile(otherID) }()
	excludedID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: fmt.Sprintf("excluded-fallback-%d", time.Now().UnixNano()), Format: "openai-responses",
		BaseURL: srv.URL, Model: "excluded-model", APIKey: "test-key", Streaming: true, Priority: 20, PoolExclude: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.pg.DeleteProfile(excludedID) }()
	if _, _, ok := s.providerForProfileEffort(excludedID, ""); !ok {
		t.Fatal("excluded profile unavailable to explicit binding")
	}
	update, err = json.Marshal(map[string]any{
		"id": excludedID, "name": "draft-excluded", "format": "openai-responses", "base_url": srv.URL,
		"model": "draft-excluded", "proxy": "://invalid", "streaming": true, "priority": 20, "pool_exclude": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	s.pgSaveProfile(recorder, httptest.NewRequest(http.MethodPost, "/api/llm/profiles", bytes.NewReader(update)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("invalid excluded profile save: %d %s", recorder.Code, recorder.Body.String())
	}
	t.Setenv("JCYBER_LLM_ROUND_ROBIN", "1")
	if err := m.SetLLMPoolEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := s.rebuildCurrentLLM(); err != nil {
		t.Fatal(err)
	}
	s.resetGlobalAutoProvider()
	for _, effort := range []string{ThinkingEffortMax, ThinkingEffortAuto} {
		prov, _, ok := s.globalProviderForEffort(effort)
		pool, poolOK := prov.(*llmpool.Pool)
		if !ok || !poolOK {
			t.Fatalf("%s effort has no failover pool: available = %v, provider = %T", effort, ok, prov)
		}
		head := pool.Head()
		if head.Name != profile.Name || head.Model != profile.Model || head.Format != profile.Format || head.Priority != profile.Priority || head.Rank != profile.Priority {
			t.Fatalf("%s effort used draft pool metadata: %+v", effort, head)
		}
		var fallback *llmpool.Member
		fallbackIndex, otherIndex := -1, -1
		for i, member := range pool.Members() {
			if member.ID == fallbackID {
				fallback = member
				fallbackIndex = i
			} else if member.ID == otherID {
				otherIndex = i
			} else if member.ID == excludedID {
				t.Fatalf("%s effort admitted applied-excluded profile: %+v", effort, member)
			}
		}
		if fallback == nil || fallback.Name != fallbackName || fallback.Model != "fallback-model" || fallback.Format != "openai-responses" || fallback.Priority != 5 || fallback.Rank != 5 {
			t.Fatalf("%s effort used draft fallback metadata: %+v", effort, fallback)
		}
		if otherIndex < 0 || fallbackIndex >= otherIndex {
			t.Fatalf("%s effort ordered draft priority ahead of applied priority: fallback index %d, other index %d", effort, fallbackIndex, otherIndex)
		}
	}
	status, ok := s.llmPoolStatus()["chain"].([]LLMPoolMemberStatus)
	if !ok {
		t.Fatal("pool status missing chain")
	}
	foundIncluded, foundExcluded := false, false
	for _, member := range status {
		switch member.ProfileID {
		case i64s(fallbackID):
			foundIncluded = true
			if member.Excluded || member.Priority != 5 || member.Name != fallbackName {
				t.Fatalf("pool status used included profile draft: %+v", member)
			}
		case i64s(excludedID):
			foundExcluded = true
			if !member.Excluded || member.Model != "excluded-model" {
				t.Fatalf("pool status used excluded profile draft: %+v", member)
			}
		}
	}
	if !foundIncluded || !foundExcluded {
		t.Fatalf("pool status omitted profiles: included=%v excluded=%v", foundIncluded, foundExcluded)
	}
}

func TestKeylessActivationSettingsRebuildsAppliedChat(t *testing.T) {
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
	stamp := time.Now().UnixNano()
	appliedID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: fmt.Sprintf("settings-applied-%d", stamp), Format: "openai-responses",
		BaseURL: srv.URL, Model: "settings-model", APIKey: "test-key", Streaming: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var keylessID int64
	defer func() {
		if previous != nil {
			_ = m.pg.SetActiveProfile(previous.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1 OR id=$2`, appliedID, keylessID)
		}
		_ = m.pg.DeleteProfile(appliedID)
		if keylessID != 0 {
			_ = m.pg.DeleteProfile(keylessID)
		}
	}()
	keylessID, err = m.pg.SaveProfile(&db.LLMProfile{
		Name: fmt.Sprintf("settings-keyless-%d", stamp), Format: "openai-responses",
		BaseURL: srv.URL, Model: "pending-model", Streaming: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	poolEnabled := m.LLMPoolEnabled()
	if err := m.SetLLMPoolEnabled(false); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.SetLLMPoolEnabled(poolEnabled) }()
	priorOn, priorBackend, priorBrave, priorTavily, priorProxy := m.WebSearch()
	if err := m.SetWebSearch(true, "ddgs", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.SetWebSearch(priorOn, priorBackend, &priorBrave, &priorTavily, &priorProxy) }()
	if err := m.pg.SetActiveProfile(appliedID); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m, llmHealth: newLLMHealthRegistry(m.pg)}
	cfg, ok := s.loadLLMConfig()
	if !ok {
		t.Fatal("applied profile unavailable")
	}
	if err := s.applyLLM(cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetActiveProfile(keylessID); err != nil {
		t.Fatal(err)
	}
	s.reapplyActiveProfile()
	chatRequest := func(sessionID string) map[string]any {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.chatAgentRef().Chat(ctx, "settings-test-agent", sessionID, "hello", 1, 0, true, nil)
		select {
		case body := <-requests:
			return body
		case <-ctx.Done():
			t.Fatal("chat did not send provider request")
			return nil
		}
	}
	hasSearch := func(body map[string]any) bool {
		tools, _ := body["tools"].([]any)
		for _, raw := range tools {
			tool, _ := raw.(map[string]any)
			if tool["name"] == "web_search" {
				return true
			}
		}
		return false
	}
	if !hasSearch(chatRequest("search-before")) {
		t.Fatal("web search missing before settings change")
	}
	recorder := httptest.NewRecorder()
	s.putSettings(recorder, httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewBufferString(`{"web_search_enabled":false}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("settings update: %d %s", recorder.Code, recorder.Body.String())
	}
	if s.llmProfileID != appliedID || s.llmCfg.Model != "settings-model" {
		t.Fatalf("settings rebuild changed applied profile: id=%d model=%s", s.llmProfileID, s.llmCfg.Model)
	}
	if hasSearch(chatRequest("search-after")) {
		t.Fatal("web search remains enabled after settings change")
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
		BaseURL: srv.URL, Model: "new-model", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	fallbackServer, fallbackRequests := effortWireServer(t)
	defer fallbackServer.Close()
	fallbackID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: fmt.Sprintf("effort-fallback-%d", stamp), Format: "openai-responses",
		BaseURL: fallbackServer.URL, Model: "fallback-model", APIKey: "test-key", Priority: 1000000,
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
		_ = m.pg.DeleteProfile(fallbackID)
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
	select {
	case request := <-fallbackRequests:
		assertEffortWire(t, request, "fallback-model", ThinkingEffortAuto)
	case <-time.After(3 * time.Second):
		t.Fatal("auto effort did not fail over to fallback profile")
	}
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
	updated, err := json.Marshal(map[string]any{
		"id": oldID, "name": fmt.Sprintf("effort-old-active-%d", stamp), "format": "openai-responses",
		"base_url": srv.URL, "model": "edited-model", "rate_per_minute": 1,
		"reasoning_effort": "high", "streaming": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	s.pgSaveProfile(recorder, httptest.NewRequest(http.MethodPost, "/api/llm/profiles", bytes.NewReader(updated)))
	if recorder.Code != http.StatusOK || s.llmCfg.Model != "edited-model" || s.llmProfileID != oldID {
		t.Fatalf("applied profile edit stayed stale: status=%d config=%s profile=%d", recorder.Code, s.llmCfg.Model, s.llmProfileID)
	}
	_, boundCfg, ok := s.providerForProfileEffort(oldID, "")
	if !ok || boundCfg.Model != "edited-model" {
		t.Fatalf("bound profile edit stayed stale: config=%+v available=%v", boundCfg, ok)
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
	s.invalidateProfileProvider(id)
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
