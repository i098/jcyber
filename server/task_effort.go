package server

import (
	"strings"

	"pta/db"
)

// Thinking effort for a task, chosen at creation time by the caller or by the
// spawner (the Auto agent / iMessage front desk):
//
//	"" / "max"  → leave the task unbound: the rotation pool drives it, and every
//	              profile in that pool already runs at max/xhigh.
//	"auto"      → bind the task to the "(auto)" variant of the active model, which
//	              sends no thinking fields, so the provider picks its own effort.
//
// Easy work should not pay for maximum reasoning; hard work keeps it.
const (
	ThinkingEffortMax  = "max"
	ThinkingEffortAuto = "auto"
)

// pickAutoProfile finds the auto variant of the active profile: same wire format,
// base URL and model, but no thinking fields of its own and excluded from the
// rotation chain (variants exist only as bind targets). It never returns the
// active profile itself, since that one carries max effort.
func pickAutoProfile(active *db.LLMProfile, profs []*db.LLMProfile) (int64, bool) {
	if active == nil {
		return 0, false
	}
	for _, p := range profs {
		if p == nil || p.ID == active.ID {
			continue
		}
		if p.Format != active.Format || p.BaseURL != active.BaseURL || p.Model != active.Model {
			continue
		}
		// The variant must genuinely be "auto": any explicit effort would make it
		// just another pinned profile, not a provider-default one.
		if strings.TrimSpace(p.ReasoningEffort) != "" || strings.TrimSpace(p.ThinkingType) != "" {
			continue
		}
		if !p.PoolExclude {
			continue
		}
		return p.ID, true
	}
	return 0, false
}

// autoEffortProfile resolves the bind target for thinking_effort=auto against the
// live profile set. ok=false means there is no auto variant for the active model,
// in which case the task simply stays on the pool.
func (s *Server) autoEffortProfile() (int64, bool) {
	if s.m == nil || s.m.pg == nil {
		return 0, false
	}
	active, err := s.m.pg.ActiveProfile()
	if err != nil {
		return 0, false
	}
	profs, err := s.m.pg.ListProfiles()
	if err != nil {
		return 0, false
	}
	return pickAutoProfile(active, profs)
}
