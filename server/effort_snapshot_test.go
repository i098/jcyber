package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"normasdk/llm"
	"pta/agent"
)

type limiterWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *limiterWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestAutoOnlyAppliedProfileBuildsMaxEffort(t *testing.T) {
	srv, requests := effortWireServer(t)
	defer srv.Close()
	base := agent.ConfigFrom("openai-responses", "applied-model", srv.URL, "test-key", "")
	base.ThinkingType, base.ReasoningEffort = "enabled", "high"
	s := &Server{m: &Manager{}}
	auto, autoConfig, ok := s.providerForEffortUnlocked(42, ThinkingEffortAuto, &base, "applied")
	if !ok || autoConfig.ThinkingType != "" || autoConfig.ReasoningEffort != "" {
		t.Fatalf("auto provider config = %+v, available = %v", autoConfig, ok)
	}
	assertEffortWire(t, effortWireRequest(t, auto, requests), base.Model, ThinkingEffortAuto)

	max, maxConfig, ok := s.providerForProfileEffort(42, ThinkingEffortMax)
	if !ok || maxConfig.Model != base.Model || maxConfig.ThinkingType != base.ThinkingType || maxConfig.ReasoningEffort != base.ReasoningEffort {
		t.Fatalf("max provider config = %+v, available = %v; want applied profile", maxConfig, ok)
	}
	assertEffortWire(t, effortWireRequest(t, max, requests), base.Model, base.ReasoningEffort)
}

func TestSelectedCallKeepsProviderAcrossSameRateEdit(t *testing.T) {
	srv, requests := effortWireServer(t)
	defer srv.Close()
	oldConfig := agent.ConfigFrom("openai-responses", "old-model", srv.URL, "test-key", "")
	oldConfig.ThinkingType, oldConfig.ReasoningEffort, oldConfig.RatePerSecond = "enabled", "high", 1
	s := &Server{m: &Manager{}}
	oldProvider, _, ok := s.providerForEffortUnlocked(42, ThinkingEffortMax, &oldConfig, "applied")
	if !ok {
		t.Fatal("old provider unavailable")
	}
	assertEffortWire(t, effortWireRequest(t, oldProvider, requests), oldConfig.Model, oldConfig.ReasoningEffort)

	baseContext, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	waitContext := &limiterWaitContext{Context: baseContext, entered: make(chan struct{})}
	finished := make(chan error, 1)
	go func() {
		_, _, _, err := oldProvider.Complete(waitContext, llm.CompletionRequest{})
		finished <- err
	}()
	select {
	case <-waitContext.entered:
	case <-baseContext.Done():
		t.Fatal("selected call did not enter shared rate limiter")
	}

	newConfig := oldConfig
	newConfig.Model, newConfig.ReasoningEffort = "new-model", "low"
	newProvider, _, ok := s.providerForEffortUnlocked(42, ThinkingEffortMax, &newConfig, "applied")
	if !ok {
		t.Fatal("new provider unavailable")
	}
	select {
	case <-finished:
	case <-baseContext.Done():
		t.Fatal("selected call did not finish")
	}
	select {
	case request := <-requests:
		assertEffortWire(t, request, oldConfig.Model, oldConfig.ReasoningEffort)
	case <-baseContext.Done():
		t.Fatal("selected call did not reach provider")
	}
	assertEffortWire(t, effortWireRequest(t, newProvider, requests), newConfig.Model, newConfig.ReasoningEffort)
}
