package server

import (
	"context"
	"errors"
	"iter"
	"log"
	"sync"

	"normasdk/llm"
	"pta/agent"
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

type effortContextKey struct{}

type profileEffortRouter struct {
	mu        sync.RWMutex
	providers map[string]llm.Provider
	limited   llm.Provider
}

func (r *profileEffortRouter) provider(ctx context.Context) llm.Provider {
	effort, _ := ctx.Value(effortContextKey{}).(string)
	r.mu.RLock()
	prov := r.providers[effort]
	r.mu.RUnlock()
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
	shared llm.Provider
	effort string
}

func (v *profileEffortView) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return v.shared.Stream(context.WithValue(ctx, effortContextKey{}, v.effort), req)
}

func (v *profileEffortView) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return v.shared.Complete(context.WithValue(ctx, effortContextKey{}, v.effort), req)
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
	if effort == ThinkingEffortMax {
		effort = ""
	}
	key := effortProfileKey{id: id, effort: effort}
	s.provCacheMu.Lock()
	var cached *provEntry
	if effort == "" {
		cached = s.provByProfile[id]
	} else {
		cached = s.provByEffort[key]
	}
	if cached != nil {
		s.provCacheMu.Unlock()
		return cached.prov, cached.cfg, true
	}
	s.provCacheMu.Unlock()
	cfg, ok := s.loadProfileConfig(id)
	if !ok {
		return nil, agent.Config{}, false
	}
	cfg = resolvedEffortConfig(cfg, effort)
	providerCfg := cfg
	providerCfg.RatePerSecond = 0
	providerCfg.RatePerMinute = 0
	prov, err := providerCfg.NewProvider()
	if err != nil {
		log.Printf("[engine] build provider for LLM profile %d failed: %v", id, err)
		return nil, agent.Config{}, false
	}
	if p, _ := s.m.pg.ProfileByID(id); p != nil {
		prov = llmrec.Wrap(prov, s.m.PG(), cfg.Model, p.Name, cfg.ThinkingType, cfg.ReasoningEffort, s.m.LLMRecordEnabled)
		prov = bindSideProvider(prov, cfg, id, p.Name)
	}
	s.provCacheMu.Lock()
	if effort == "" {
		cached = s.provByProfile[id]
	} else {
		cached = s.provByEffort[key]
	}
	if cached != nil {
		prov, cfg = cached.prov, cached.cfg
	} else {
		shared := s.provShared[id]
		if shared == nil {
			shared = &profileEffortRouter{providers: map[string]llm.Provider{}}
			shared.limited = llm.RateLimited(shared, llm.RateLimit{PerSecond: cfg.RatePerSecond, PerMinute: cfg.RatePerMinute})
			if s.provShared == nil {
				s.provShared = map[int64]*profileEffortRouter{}
			}
			s.provShared[id] = shared
		}
		shared.mu.Lock()
		shared.providers[effort] = prov
		shared.mu.Unlock()
		prov = &profileEffortView{shared: shared.limited, effort: effort}
		entry := &provEntry{prov: prov, cfg: cfg}
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
		log.Printf("[engine] built provider for LLM profile %d (%s / %s / %s)", id, cfg.Provider(), cfg.Model, effort)
	}
	s.provCacheMu.Unlock()
	return prov, cfg, true
}

func (s *Server) environmentProviderForEffort(baseCfg agent.Config, name, effort string) (llm.Provider, agent.Config, bool) {
	key := effortProfileKey{effort: effort}
	s.provCacheMu.Lock()
	var cached *provEntry
	if effort == "" {
		cached = s.provByProfile[0]
	} else {
		cached = s.provByEffort[key]
	}
	if cached != nil {
		s.provCacheMu.Unlock()
		return cached.prov, cached.cfg, true
	}
	s.provCacheMu.Unlock()
	cfg := resolvedEffortConfig(baseCfg, effort)
	providerCfg := cfg
	providerCfg.RatePerSecond = 0
	providerCfg.RatePerMinute = 0
	prov, err := providerCfg.NewProvider()
	if err != nil {
		return nil, agent.Config{}, false
	}
	prov = llmrec.Wrap(prov, s.m.PG(), cfg.Model, name, cfg.ThinkingType, cfg.ReasoningEffort, s.m.LLMRecordEnabled)
	prov = bindSideProvider(prov, cfg, 0, name)
	s.provCacheMu.Lock()
	if effort == "" {
		cached = s.provByProfile[0]
	} else {
		cached = s.provByEffort[key]
	}
	if cached != nil {
		prov, cfg = cached.prov, cached.cfg
	} else {
		shared := s.provShared[0]
		if shared == nil {
			shared = &profileEffortRouter{providers: map[string]llm.Provider{}}
			shared.limited = llm.RateLimited(shared, llm.RateLimit{PerSecond: cfg.RatePerSecond, PerMinute: cfg.RatePerMinute})
			if s.provShared == nil {
				s.provShared = map[int64]*profileEffortRouter{}
			}
			s.provShared[0] = shared
		}
		shared.mu.Lock()
		shared.providers[effort] = prov
		shared.mu.Unlock()
		prov = &profileEffortView{shared: shared.limited, effort: effort}
		entry := &provEntry{prov: prov, cfg: cfg}
		if effort == "" {
			if s.provByProfile == nil {
				s.provByProfile = map[int64]*provEntry{}
			}
			s.provByProfile[0] = entry
		} else {
			if s.provByEffort == nil {
				s.provByEffort = map[effortProfileKey]*provEntry{}
			}
			s.provByEffort[key] = entry
		}
	}
	s.provCacheMu.Unlock()
	return prov, cfg, true
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
	delete(s.provByEffort, effortProfileKey{effort: ThinkingEffortAuto})
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
	var prov llm.Provider
	var cfg agent.Config
	var poolHeadID int64
	if s.m != nil && s.m.pg != nil && appliedProfileID != 0 {
		if s.profileConfigMatches(appliedProfileID, baseCfg) {
			prov, cfg, _ = s.providerForProfileEffortUnlocked(appliedProfileID, effort)
			if prov != nil && cfg == resolvedEffortConfig(baseCfg, effort) {
				if active, err := s.m.pg.ActiveProfile(); err == nil && active != nil && active.ID == appliedProfileID {
					poolHeadID = appliedProfileID
				}
			} else {
				prov = nil
			}
		}
	}
	if prov == nil {
		if s.m == nil {
			return nil, agent.Config{}, false
		}
		var ok bool
		prov, cfg, ok = s.environmentProviderForEffort(baseCfg, name, effort)
		if !ok {
			return nil, agent.Config{}, false
		}
	}
	if withFailover && poolHeadID != 0 {
		prov = s.poolForActiveEffort(poolHeadID, prov, cfg, effort)
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
		s.globalByEffort[effort] = &provEntry{prov: prov, cfg: cfg}
	}
	s.provCacheMu.Unlock()
	return prov, cfg, true
}
