package llmpool

import (
	"testing"

	"normasdk/llm"
)

// TestEqualRankMembersRotatePerCall is the deployed rotation config in miniature:
// three profiles at the same priority (rank 100, which is what headRank returns in
// round-robin mode). Successive order() calls must let each of them lead once,
// instead of pinning the chain head on every call.
func TestEqualRankMembersRotatePerCall(t *testing.T) {
	mk := func(id int64, name string) *Member {
		return &Member{ID: id, Name: name, Rank: 100, WindowTokens: 1_000_000}
	}
	p := New([]*Member{mk(17, "codex"), mk(16, "deepseek"), mk(19, "kimi")}, nil)

	led := map[int64]int{}
	for i := range 3 {
		order := p.order(llm.CompletionRequest{})
		if len(order) != 3 {
			t.Fatalf("call %d: got %d members, want all 3", i, len(order))
		}
		led[order[0].ID]++
	}
	if len(led) != 3 {
		t.Fatalf("expected all three profiles to lead across 3 calls, got %v", led)
	}
	for id, n := range led {
		if n != 1 {
			t.Fatalf("profile %d led %d times across 3 calls, want exactly once", id, n)
		}
	}
}
