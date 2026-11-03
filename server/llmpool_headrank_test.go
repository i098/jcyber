package server

import (
	"testing"

	"pta/llmpool"
)

// TestHeadRank locks the two chain-head behaviors: default keeps the active
// profile at the top rank (failover), while the env flag drops it to its own
// priority so equal-priority profiles round-robin per call.
func TestHeadRank(t *testing.T) {
	t.Setenv("JCYBER_LLM_ROUND_ROBIN", "")
	if got := headRank(100); got != llmpool.RankActive {
		t.Fatalf("default head rank = %d, want RankActive", got)
	}
	t.Setenv("JCYBER_LLM_ROUND_ROBIN", "1")
	if got := headRank(100); got != 100 {
		t.Fatalf("round-robin head rank = %d, want the profile priority 100", got)
	}
	t.Setenv("JCYBER_LLM_ROUND_ROBIN", "off")
	if got := headRank(100); got != llmpool.RankActive {
		t.Fatalf("off should keep RankActive, got %d", got)
	}
}
