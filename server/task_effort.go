package server

import (
	"context"
	"errors"
	"iter"
	"log"

	"normasdk/llm"
	"pta/agent"
	"pta/db"
	"pta/llmrec"
)

const (
	ThinkingEffortMax  = "max"
	ThinkingEffortAuto = "auto"
)

type effortProfileKey struct {
	id     int64
	effort string
}

type providerContextKey struct{}

type profileEffortRouter struct {
	limited    llm.Provider
	rateSecond float64
	rateMinute float64
}

func (*profileEffortRouter) provider(ctx context.Context) llm.Provider {
	prov, _ := ctx.Value(providerContextKey{}).(llm.Provider)
	return prov
}

func (r *profileEffortRouter) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	if prov := r.provider(ctx); prov != nil {
		return prov.Stream(ctx, req)
	}
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("LLM effort provider unavailable"))
	}
}

func (r *profileEffortRouter) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if prov := r.provider(ctx); prov != nil {
		return prov.Complete(ctx, req)
	}
	return llm.Message{}, "", llm.Usage{}, errors.New("LLM effort provider unavailable")
}

type profileEffortView struct {
	shared   llm.Provider
	provider llm.Provider
}

func (v *profileEffortView) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return v.shared.Stream(context.WithValue(ctx, providerContextKey{}, v.provider), req)
}

func (v *profileEffortView) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return v.shared.Complete(context.WithValue(ctx, providerContextKey{}, v.provider), req)
}

func resolvedEffortConfig(cfg agent.Config, effort string) agent.Config {
	if effort == ThinkingEffortAuto {
		cfg.ThinkingType = ""
		cfg.ReasoningEffort = ""
	}
	return cfg
}

func (s *Server) providerForProfileEffort(id int64, effort string) (llm.Provider, agent.Config, bool) {
	s.profileReapplyMu.RLock()
	defer s.profileReapplyMu.RUnlock()
	return s.providerForProfileEffortUnlocked(id, effort)
}

func (s *Server) providerForProfileEffortUnlocked(id int64, effort string) (llm.Provider, agent.Config, bool) {
	return s.providerForEffortUnlocked(id, effort, nil, "")
}

func (s *Server) cachedProfileEntry(id int64, effort string) *provEntry {
	if effort == ThinkingEffortMax {
		effort = ""
	}
	s.provCacheMu.Lock()
	defer s.provCacheMu.Unlock()
	if effort != "" {
		if entry := s.provByEffort[effortProfileKey{id: id, effort: effort}]; entry != nil {
			return entry
		}
	}
	if entry := s.provByProfile[id]; entry != nil {
		return entry
	}
	return s.provByEffort[effortProfileKey{id: id, effort: ThinkingEffortAuto}]
}

func (s *Server) buildEffortProvider(id int64, baseCfg agent.Config, name, effort string) (llm.Provider, agent.Config, error) {
	cfg := resolvedEffortConfig(baseCfg, effort)
	providerCfg := cfg
	providerCfg.RatePerSecond, providerCfg.RatePerMinute = 0, 0
	prov, err := providerCfg.NewProvider()
	if err != nil {
		return nil, agent.Config{}, err
	}
	prov = llmrec.Wrap(prov, s.m.PG(), cfg.Model, name, cfg.ThinkingType, cfg.ReasoningEffort, s.m.LLMRecordEnabled)
	return bindSideProvider(prov, cfg, id, name), cfg, nil
}

func (s *Server) providerForEffortUnlocked(id int64, effort string, source *agent.Config, name string) (llm.Provider, agent.Config, bool) {
	if effort == ThinkingEffortMax {
		effort = ""
	}
	key := effortProfileKey{id: id, effort: effort}
	var desired agent.Config
	priority := 0
	poolExclude := false
	var retryOverride db.RetryOverride
	if source != nil {
		desired = resolvedEffortConfig(*source, effort)
	}
	s.provCacheMu.Lock()
	var cached *provEntry
	if effort == "" {
		cached = s.provByProfile[id]
	} else {
		cached = s.provByEffort[key]
	}
	if cached != nil && (source == nil || cached.cfg == desired) {
		s.provCacheMu.Unlock()
		return cached.prov, cached.cfg, true
	}
	base := s.provByProfile[id]
	if base == nil {
		base = s.provByEffort[effortProfileKey{id: id, effort: ThinkingEffortAuto}]
	}
	if base != nil {
		priority = base.priority
		poolExclude = base.poolExclude
		retryOverride = base.retryOverride
		if source == nil {
			applied := base.baseCfg
			source = &applied
			name = base.name
			desired = resolvedEffortConfig(applied, effort)
		}
	}
	s.provCacheMu.Unlock()
	var baseCfg agent.Config
	if source == nil {
		var ok bool
		baseCfg, ok = s.loadProfileConfig(id)
		if !ok {
			return nil, agent.Config{}, false
		}
		profile, err := s.m.pg.ProfileByID(id)
		if err != nil || profile == nil {
			return nil, agent.Config{}, false
		}
		name = profile.Name
		priority = profile.Priority
		poolExclude = profile.PoolExclude
		retryOverride = profile.Retry
	} else {
		baseCfg = *source
	}
	prov, cfg, err := s.buildEffortProvider(id, baseCfg, name, effort)
	if err != nil {
		if id != 0 {
			log.Printf("[engine] build provider for LLM profile %d failed: %v", id, err)
		}
		return nil, agent.Config{}, false
	}
	s.provCacheMu.Lock()
	if effort == "" {
		cached = s.provByProfile[id]
	} else {
		cached = s.provByEffort[key]
	}
	if cached != nil && (source == nil || cached.cfg == cfg) {
		prov, cfg = cached.prov, cached.cfg
	} else {
		shared := s.provShared[id]
		if shared != nil && (shared.rateSecond != cfg.RatePerSecond || shared.rateMinute != cfg.RatePerMinute) {
			delete(s.provByProfile, id)
			for other := range s.provByEffort {
				if other.id == id {
					delete(s.provByEffort, other)
				}
			}
			shared = nil
		}
		if shared == nil {
			shared = &profileEffortRouter{rateSecond: cfg.RatePerSecond, rateMinute: cfg.RatePerMinute}
			shared.limited = llm.RateLimited(shared, llm.RateLimit{PerSecond: cfg.RatePerSecond, PerMinute: cfg.RatePerMinute})
			if s.provShared == nil {
				s.provShared = map[int64]*profileEffortRouter{}
			}
			s.provShared[id] = shared
		}
		prov = &profileEffortView{shared: shared.limited, provider: prov}
		entry := &provEntry{prov: prov, cfg: cfg, baseCfg: baseCfg, retryOverride: retryOverride, name: name, priority: priority, poolExclude: poolExclude}
		if effort == "" {
			if s.provByProfile == nil {
				s.provByProfile = map[int64]*provEntry{}
			}
			s.provByProfile[id] = entry
		} else {
			if s.provByEffort == nil {
				s.provByEffort = map[effortProfileKey]*provEntry{}
			}
			s.provByEffort[key] = entry
		}
		if id != 0 {
			log.Printf("[engine] built provider for LLM profile %d (%s / %s / %s)", id, cfg.Provider(), cfg.Model, effort)
		}
	}
	s.provCacheMu.Unlock()
	return prov, cfg, true
}

func (s *Server) refreshRetryPolicyProviders() bool {
	s.cfgMu.Lock()
	appliedID := s.llmProfileID
	s.cfgMu.Unlock()
	s.provCacheMu.Lock()
	variants := map[int64]map[string]*provEntry{}
	for id, entry := range s.provByProfile {
		if id != 0 {
			variants[id] = map[string]*provEntry{"": entry}
		}
	}
	for key, entry := range s.provByEffort {
		if key.id != 0 {
			if variants[key.id] == nil {
				variants[key.id] = map[string]*provEntry{}
			}
			variants[key.id][key.effort] = entry
		}
	}
	s.provCacheMu.Unlock()
	type replacement struct {
		prov llm.Provider
		cfg  agent.Config
	}
	refreshed := false
	var appliedGlobal *provEntry
	policy := s.retryPolicy()
	for id, entries := range variants {
		applied := entries[""]
		if applied == nil {
			applied = entries[ThinkingEffortAuto]
		}
		baseCfg := applied.baseCfg
		baseCfg.Retry = resolveRetry(applied.retryOverride, policy)
		staged := make(map[string]replacement, len(entries))
		for effort := range entries {
			prov, cfg, err := s.buildEffortProvider(id, baseCfg, applied.name, effort)
			if err != nil {
				log.Printf("[engine] rebuild provider for LLM profile %d failed: %v", id, err)
				staged = nil
				break
			}
			staged[effort] = replacement{prov: prov, cfg: cfg}
		}
		if staged == nil {
			continue
		}
		s.provCacheMu.Lock()
		shared := s.provShared[id]
		if shared == nil || shared.rateSecond != baseCfg.RatePerSecond || shared.rateMinute != baseCfg.RatePerMinute {
			s.provCacheMu.Unlock()
			continue
		}
		for effort, built := range staged {
			entry := &provEntry{
				prov: &profileEffortView{shared: shared.limited, provider: built.prov}, cfg: built.cfg,
				baseCfg: baseCfg, retryOverride: applied.retryOverride,
				name: applied.name, priority: applied.priority, poolExclude: applied.poolExclude,
			}
			if effort == "" {
				s.provByProfile[id] = entry
				if id == appliedID {
					appliedGlobal = entry
				}
			} else {
				key := effortProfileKey{id: id, effort: effort}
				s.provByEffort[key] = entry
			}
		}
		s.provCacheMu.Unlock()
		s.profMu.Lock()
		delete(s.profChatAgents, id)
		s.profMu.Unlock()
		refreshed = true
	}
	if refreshed {
		s.provCacheMu.Lock()
		s.globalByEffort = map[string]*provEntry{}
		s.provCacheMu.Unlock()
	}
	if appliedGlobal != nil {
		s.cfgMu.Lock()
		if s.llmOn && s.llmProfileID == appliedID {
			s.llmCfg = appliedGlobal.cfg
			s.llmDirect = appliedGlobal.prov
		}
		s.cfgMu.Unlock()
	}
	return refreshed
}

func (s *Server) resetEnvironmentEffortProviders() {
	s.provCacheMu.Lock()
	delete(s.provByProfile, 0)
	delete(s.provShared, 0)
	delete(s.provByEffort, effortProfileKey{effort: ThinkingEffortAuto})
	s.globalByEffort = map[string]*provEntry{}
	s.provCacheMu.Unlock()
}

func (s *Server) resetGlobalAutoProvider() {
	s.provCacheMu.Lock()
	delete(s.provByEffort, effortProfileKey{id: s.llmProfileID, effort: ThinkingEffortAuto})
	delete(s.globalByEffort, ThinkingEffortAuto)
	s.provCacheMu.Unlock()
}

func (s *Server) globalProviderForEffort(effort string) (llm.Provider, agent.Config, bool) {
	s.profileReapplyMu.RLock()
	defer s.profileReapplyMu.RUnlock()
	return s.resolveGlobalProviderForEffort(effort, true)
}

func (s *Server) globalDirectAutoProvider() (llm.Provider, agent.Config, bool) {
	s.profileReapplyMu.RLock()
	defer s.profileReapplyMu.RUnlock()
	return s.resolveGlobalProviderForEffort(ThinkingEffortAuto, false)
}

func (s *Server) resolveGlobalProviderForEffort(effort string, withFailover bool) (llm.Provider, agent.Config, bool) {
	if effort == "" || effort == ThinkingEffortMax {
		return s.globalProvider()
	}
	s.cfgMu.Lock()
	on, baseCfg, name, appliedProfileID := s.llmOn, s.llmCfg, s.llmProf, s.llmProfileID
	if !on {
		s.cfgMu.Unlock()
		return nil, agent.Config{}, false
	}
	s.provCacheMu.Lock()
	if withFailover {
		if e := s.globalByEffort[effort]; e != nil {
			s.provCacheMu.Unlock()
			s.cfgMu.Unlock()
			return e.prov, e.cfg, true
		}
	}
	s.provCacheMu.Unlock()
	s.cfgMu.Unlock()
	if s.m == nil {
		return nil, agent.Config{}, false
	}
	prov, cfg, ok := s.providerForEffortUnlocked(appliedProfileID, effort, &baseCfg, name)
	if !ok {
		return nil, agent.Config{}, false
	}
	if withFailover && appliedProfileID != 0 {
		prov = s.poolForActiveEffort(appliedProfileID, prov, cfg, effort)
	}
	s.provCacheMu.Lock()
	if !withFailover {
		s.provCacheMu.Unlock()
		return prov, cfg, true
	}
	if e := s.globalByEffort[effort]; e != nil {
		prov, cfg = e.prov, e.cfg
	} else {
		if s.globalByEffort == nil {
			s.globalByEffort = map[string]*provEntry{}
		}
		s.globalByEffort[effort] = &provEntry{prov: prov, cfg: cfg, name: name}
	}
	s.provCacheMu.Unlock()
	return prov, cfg, true
}
