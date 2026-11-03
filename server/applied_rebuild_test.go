package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"normasdk/llm"
	"pta/db"
	"pta/llmpool"
)

func saveAppliedRebuildProfile(t *testing.T, s *Server, profile map[string]any) {
	t.Helper()
	body, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.pgSaveProfile(response, httptest.NewRequest(http.MethodPost, "/api/llm/profiles", bytes.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("save profile: %d %s", response.Code, response.Body.String())
	}
}

func TestFallbackEditRebuildsAppliedPoolAfterInvalidDraft(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	activeServer, _ := effortWireServer(t)
	defer activeServer.Close()
	oldFallbackServer, oldRequests := effortWireServer(t)
	defer oldFallbackServer.Close()
	newFallbackServer, newRequests := effortWireServer(t)
	defer newFallbackServer.Close()
	previous, err := m.pg.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UnixNano()
	active := &db.LLMProfile{
		Name: fmt.Sprintf("applied-head-%d", stamp), Format: "openai-responses",
		BaseURL: activeServer.URL, Model: "applied-head-model", APIKey: "test-key", Streaming: true,
	}
	activeID, err := m.pg.SaveProfile(active)
	if err != nil {
		t.Fatal(err)
	}
	fallback := &db.LLMProfile{
		Name: fmt.Sprintf("fallback-edit-%d", stamp), Format: "openai-responses",
		BaseURL: oldFallbackServer.URL, Model: "old-fallback-model", APIKey: "test-key", Streaming: true, Priority: 1000,
	}
	fallbackID, err := m.pg.SaveProfile(fallback)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if previous != nil {
			_ = m.pg.SetActiveProfile(previous.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, activeID)
		}
		_ = m.pg.DeleteProfile(fallbackID)
		_ = m.pg.DeleteProfile(activeID)
	}()
	if err := m.pg.SetActiveProfile(activeID); err != nil {
		t.Fatal(err)
	}
	previousPool := m.LLMPoolEnabled()
	if err := m.SetLLMPoolEnabled(true); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.SetLLMPoolEnabled(previousPool) }()
	t.Setenv("JCYBER_LLM_ROUND_ROBIN", "")
	s := &Server{m: m, llmHealth: newLLMHealthRegistry(m.pg)}
	cfg, ok := s.loadLLMConfig()
	if !ok {
		t.Fatal("active profile unavailable")
	}
	if err := s.applyLLM(cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.llmProv.(*llmpool.Pool); !ok {
		t.Fatalf("initial failover pool unavailable: %T", s.llmProv)
	}
	saveAppliedRebuildProfile(t, s, map[string]any{
		"id": activeID, "name": "draft-" + active.Name, "format": active.Format,
		"base_url": active.BaseURL, "model": "draft-head-model", "proxy": "://invalid", "streaming": true,
	})
	saveAppliedRebuildProfile(t, s, map[string]any{
		"id": fallbackID, "name": fallback.Name, "format": fallback.Format,
		"base_url": newFallbackServer.URL, "model": "new-fallback-model", "streaming": true, "priority": fallback.Priority,
	})
	if s.llmProfileID != activeID || s.llmCfg.Model != active.Model {
		t.Fatalf("global provider changed to draft: profile=%d model=%s", s.llmProfileID, s.llmCfg.Model)
	}
	pool, ok := s.llmProv.(*llmpool.Pool)
	if !ok {
		t.Fatalf("global failover pool unavailable after fallback edit: %T", s.llmProv)
	}
	var member *llmpool.Member
	for _, candidate := range pool.Members() {
		if candidate.ID == fallbackID {
			member = candidate
			break
		}
	}
	if member == nil {
		t.Fatal("edited fallback missing from global pool")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, _, _ = member.Prov.Complete(ctx, llm.CompletionRequest{})
	select {
	case request := <-newRequests:
		if request["model"] != "new-fallback-model" {
			t.Fatalf("fallback request model = %v", request["model"])
		}
	case request := <-oldRequests:
		t.Fatalf("global pool used old fallback endpoint: %v", request)
	case <-ctx.Done():
		t.Fatal("edited fallback sent no request")
	}
}

func TestRetryPolicyRefreshesAppliedProviderAfterInvalidDraft(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	var attempts atomic.Int32
	requests := make(chan map[string]any, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		body, _ := io.ReadAll(r.Body)
		var request map[string]any
		if json.Unmarshal(body, &request) == nil {
			requests <- request
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	previous, err := m.pg.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	previousPolicy := m.pg.LLMRetryPolicy()
	defer func() { _ = m.pg.SetLLMRetryPolicy(previousPolicy) }()
	profile := &db.LLMProfile{
		Name: fmt.Sprintf("applied-retry-%d", time.Now().UnixNano()), Format: "openai-responses",
		BaseURL: srv.URL, Model: "applied-retry-model", APIKey: "test-key", Streaming: true,
		Retry: db.RetryOverride{Empty: db.RetryRule{Attempts: 7}},
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
	previousPool := m.LLMPoolEnabled()
	if err := m.SetLLMPoolEnabled(false); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.SetLLMPoolEnabled(previousPool) }()
	s := &Server{m: m, llmHealth: newLLMHealthRegistry(m.pg)}
	cfg, ok := s.loadLLMConfig()
	if !ok {
		t.Fatal("active profile unavailable")
	}
	if err := s.applyLLM(cfg); err != nil {
		t.Fatal(err)
	}
	saveAppliedRebuildProfile(t, s, map[string]any{
		"id": id, "name": "draft-" + profile.Name, "format": profile.Format,
		"base_url": profile.BaseURL, "model": "draft-retry-model", "proxy": "://invalid", "streaming": true,
		"retry": map[string]any{"connect": map[string]any{"attempts": 4}, "empty": map[string]any{"attempts": 9}},
	})
	policy, err := json.Marshal(db.LLMRetryPolicy{
		Connect: db.RetryRule{Attempts: -1, IntervalMS: 1},
		Empty:   db.RetryRule{Attempts: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.pgSaveLLMRetryPolicy(response, httptest.NewRequest(http.MethodPost, "/api/llm/retry-policy", bytes.NewReader(policy)))
	if response.Code != http.StatusOK {
		t.Fatalf("save retry policy: %d %s", response.Code, response.Body.String())
	}
	provider, applied, ok := s.globalProviderForEffort(ThinkingEffortMax)
	if !ok || applied.Model != profile.Model || applied.Retry.ConnectAttempts != -1 || applied.Retry.EmptyAttempts != 7 {
		t.Fatalf("global applied retry config = %+v, available = %v", applied, ok)
	}
	_, auto, ok := s.globalProviderForEffort(ThinkingEffortAuto)
	if !ok || auto.Model != profile.Model || auto.Retry.ConnectAttempts != -1 || auto.Retry.EmptyAttempts != 7 {
		t.Fatalf("auto applied retry config = %+v, available = %v", auto, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, _, err = provider.Complete(ctx, llm.CompletionRequest{})
	if err == nil {
		t.Fatal("provider unexpectedly accepted HTTP 503")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("applied provider made %d requests with retries disabled; want 1", got)
	}
	select {
	case request := <-requests:
		if request["model"] != profile.Model {
			t.Fatalf("applied request model = %v, want %s", request["model"], profile.Model)
		}
	default:
		t.Fatal("applied provider sent no request")
	}
}
