package server

import (
	"testing"

	"pta/db"
)

// TestPickAutoProfile covers the effort routing used when a task is created with
// thinking_effort=auto: it must land on the provider-default variant of the ACTIVE
// model only — same format, base URL and model, no thinking fields, excluded from
// the rotation chain. Anything pinned to an effort, on another model, or in the
// pool is not an auto variant.
func TestPickAutoProfile(t *testing.T) {
	active := &db.LLMProfile{
		ID: 17, Format: "openai-responses", BaseURL: "https://chatgpt.com/backend-api/codex",
		Model: "gpt-6-sol", ReasoningEffort: "max",
	}
	auto := &db.LLMProfile{
		ID: 20, Format: active.Format, BaseURL: active.BaseURL, Model: active.Model, PoolExclude: true,
	}
	profs := []*db.LLMProfile{
		{ID: 16, Format: "openai", BaseURL: "https://api.deepseekv4pro.com/v1", Model: "deepseek-flash", ReasoningEffort: "xhigh"},
		active,
		auto,
		{ID: 21, Format: active.Format, BaseURL: active.BaseURL, Model: active.Model, PoolExclude: true, ReasoningEffort: "high"},
		{ID: 22, Format: active.Format, BaseURL: active.BaseURL, Model: "other-model", PoolExclude: true},
		{ID: 23, Format: active.Format, BaseURL: active.BaseURL, Model: active.Model, PoolExclude: false},
	}

	if id, ok := pickAutoProfile(active, profs); !ok || id != 20 {
		t.Fatalf("pickAutoProfile = (%d, %v), want (20, true)", id, ok)
	}

	// Same profiles without the auto variant: no bind target, task stays on the pool.
	if _, ok := pickAutoProfile(active, []*db.LLMProfile{active, profs[0], profs[3], profs[4]}); ok {
		t.Fatal("expected no auto variant when every candidate has an effort, another model, or pool membership")
	}

	if _, ok := pickAutoProfile(nil, profs); ok {
		t.Fatal("nil active profile must not bind anything")
	}
}
