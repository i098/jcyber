package server

import (
	"fmt"
	"net/http"

	"pta/db"
)

type taskLLMResolution struct {
	ProfileID *int64 `json:"profile_id,omitempty"`
	Name      string `json:"name"`
	Format    string `json:"format"`
	Model     string `json:"model"`
	Source    string `json:"source"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

func (s *Server) resolutionFromProfile(p *db.LLMProfile, source, effort string) taskLLMResolution {
	if p == nil {
		return taskLLMResolution{Source: source, Reason: "LLM profile does not exist"}
	}
	id := p.ID
	result := taskLLMResolution{
		ProfileID: &id,
		Name:      p.Name,
		Format:    p.Format,
		Model:     p.Model,
		Source:    source,
	}
	s.profileReapplyMu.RLock()
	_, cfg, ok := s.providerForProfileEffortUnlocked(p.ID, effort)
	if ok {
		if cached := s.cachedProfileEntry(p.ID, effort); cached != nil && cached.name != "" {
			result.Name = cached.name
		}
	}
	s.profileReapplyMu.RUnlock()
	if !ok {
		if p.APIKey == "" {
			result.Reason = "LLM profile has no API key"
		} else {
			result.Reason = "Invalid or unsupported LLM profile format"
		}
		return result
	}
	result.Format, result.Model = cfg.Provider(), cfg.Model
	result.Available = true
	return result
}

// resolveTaskRoleLLM mirrors taskLLMRuntime.current without creating a provider:
// role Agent binding -> explicit task chain -> active/global environment config.
// Database failures are returned rather than represented as an unavailable model.
func (s *Server) resolveTaskRoleLLM(t *Task, agentKey string) (taskLLMResolution, error) {
	if s == nil || s.m == nil || s.m.pg == nil {
		return taskLLMResolution{}, fmt.Errorf("database unavailable")
	}
	a, err := s.m.pg.GetAgentByKey(agentKey)
	if err != nil {
		return taskLLMResolution{}, fmt.Errorf("load agent %q: %w", agentKey, err)
	}
	if a != nil && a.LLMProfileID != nil {
		p, err := s.m.pg.ProfileByID(*a.LLMProfileID)
		if err != nil {
			return taskLLMResolution{}, fmt.Errorf("load agent LLM profile: %w", err)
		}
		if p != nil {
			resolved := s.resolutionFromProfile(p, "agent_binding", t.ThinkingEffort)
			if resolved.Available {
				return resolved, nil
			}
		}
	}
	state := t.llmStateSnapshot()
	if len(state.ProfileIDs) > 0 {
		if state.ActiveID == nil {
			return taskLLMResolution{Source: "task_chain", Reason: "Task LLM profile chain exhausted"}, nil
		}
		p, err := s.m.pg.ProfileByID(*state.ActiveID)
		if err != nil {
			return taskLLMResolution{}, fmt.Errorf("load task LLM profile: %w", err)
		}
		return s.resolutionFromProfile(p, "task_chain", t.ThinkingEffort), nil
	}
	s.cfgMu.Lock()
	on, name, cfg, appliedID := s.llmOn, s.llmProf, s.llmCfg, s.llmProfileID
	s.cfgMu.Unlock()
	if on {
		if name == "" {
			name = "Global Profile"
		}
		resolved := taskLLMResolution{
			Name: name, Format: cfg.Provider(), Model: cfg.Model,
			Source: "environment", Available: true,
		}
		if appliedID != 0 {
			resolved.ProfileID = &appliedID
			resolved.Source = "global_profile"
		}
		return resolved, nil
	}
	return taskLLMResolution{Source: "global", Reason: "No available LLM profile"}, nil
}

func (s *Server) taskLLMResolutionHandler(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	roles := map[string]taskLLMResolution{}
	for responseKey, agentKey := range map[string]string{
		"mainagent": "mainagent",
		"planner":   "planner",
		"worker":    "worker",
	} {
		resolved, err := s.resolveTaskRoleLLM(t, agentKey)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		roles[responseKey] = resolved
	}
	writeJSON(w, http.StatusOK, roles)
}
