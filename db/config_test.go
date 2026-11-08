package db

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// TestPoolProfilesOrder pins the failover chain query: keyless profiles can't
// serve a request and excluded ones aren't fallback targets, so neither belongs
// in the chain; the rest come back by priority, highest first.
// Deliberately does NOT touch is_default — flipping the active profile would be a
// side effect on the shared dev database.
func TestPoolProfilesOrder(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	mk := func(name string, priority int, exclude bool, key string) int64 {
		id, err := d.SaveProfile(&LLMProfile{
			Name: name, Format: "openai", Model: "m", APIKey: key,
			Priority: priority, PoolExclude: exclude,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
		return id
	}
	lo := mk("t-pool-lo", 1, false, "k1")
	hi := mk("t-pool-hi", 9, false, "k2")
	mk("t-pool-excluded", 99, true, "k3") // excluded despite the top priority
	mk("t-pool-nokey", 50, false, "")     // no key → cannot serve anything

	chain, err := d.PoolProfiles()
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, p := range chain {
		switch p.Name {
		case "t-pool-lo", "t-pool-hi":
			got = append(got, p.ID)
		case "t-pool-excluded":
			t.Fatal("pool_exclude profile entered the failover chain")
		case "t-pool-nokey":
			t.Fatal("keyless profile entered the failover chain")
		}
	}
	if len(got) != 2 || got[0] != hi || got[1] != lo {
		t.Fatalf("chain order = %v, want [hi=%d lo=%d]", got, hi, lo)
	}
}

func TestDeleteProfileContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var d DB
	if err := d.DeleteProfileContext(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteProfileContext error=%v, want context cancellation", err)
	}
}

func TestConfigStores(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	// LLM profile: save, active, key never serialized
	pid, err := d.SaveProfile(&LLMProfile{Name: "t-default", Format: "openai", Model: "gpt-x", APIKey: "secret123"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, pid)
	if err := d.SetActiveProfile(pid); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteProfile(pid); !errors.Is(err, ErrActiveLLMProfileDelete) {
		t.Fatalf("deleting active profile error=%v, want %v", err, ErrActiveLLMProfileDelete)
	}
	act, err := d.ActiveProfile()
	if err != nil || act == nil || act.APIKey != "secret123" {
		t.Fatalf("active profile/key: %+v err=%v", act, err)
	}
	// list must hide the key, expose hint
	list, _ := d.ListProfiles()
	for _, p := range list {
		if p.ID == pid {
			b, _ := json.Marshal(p)
			if string(b) == "" || contains(string(b), "secret123") {
				t.Fatalf("api key leaked in list json: %s", b)
			}
			if p.APIKeyHint != "…t123" {
				t.Fatalf("hint want …t123, got %q", p.APIKeyHint)
			}
		}
	}

	// agents seeded; prompt versioning
	ag, err := d.GetAgentByKey("planner")
	if err != nil || ag == nil {
		t.Fatalf("planner agent: %v", err)
	}
	v1, err := d.SavePrompt(ag.ID, "你是规划者 {{.Goal}}", "init", "test")
	if err != nil {
		t.Fatal(err)
	}
	v2, _ := d.SavePrompt(ag.ID, "你是规划者 v2 {{.Goal}} {{.Scope}}", "edit", "test")
	if v2 != v1+1 {
		t.Fatalf("version should increment: %d -> %d", v1, v2)
	}
	cur, _ := d.CurrentPrompt(ag.ID)
	if cur != "你是规划者 v2 {{.Goal}} {{.Scope}}" {
		t.Fatalf("current prompt wrong: %q", cur)
	}
	vers, _ := d.ListPromptVersions(ag.ID)
	if len(vers) < 2 {
		t.Fatalf("want >=2 versions, got %d", len(vers))
	}
	pv, _ := d.PromptVars(ag.ID)
	// planner must have at least the seeded catalog vars (Goal, AssetSummary)
	if len(pv) < 2 {
		t.Fatalf("planner catalog want >=2 vars, got %d", len(pv))
	}
	d.Exec(`DELETE FROM agent_prompts WHERE agent_id=$1`, ag.ID)
	d.Exec(`UPDATE agents SET current_prompt_id=NULL WHERE id=$1`, ag.ID)

	// mcp + skill + visibility (bidirectional via one join)
	// Clean up any leftover MCP from prior runs to keep this test idempotent.
	d.Exec(`DELETE FROM mcp_servers WHERE name = 't-gh'`)
	mid, err := d.SaveMCP(&MCPServer{Name: "t-gh", Transport: "stdio", Command: "npx", Args: json.RawMessage(`["server-github"]`), Env: json.RawMessage(`{"GITHUB_TOKEN":"x"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// agent-side write. MCP is id-keyed (generic visibility join); skills are now
	// filesystem-based, so their visibility is keyed by skill (directory) name in a
	// dedicated table. Clear any pre-existing MCP visibility rows so the assertions
	// below isolate on exactly what this test sets.
	d.Exec(`DELETE FROM agent_visibility WHERE agent_id = $1 AND resource_kind = 'mcp'`, ag.ID)
	if err := d.ToggleVisibility(ag.ID, "mcp", mid, true); err != nil {
		t.Fatal(err)
	}
	if err := d.SetAgentSkillVisibility(ag.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.ToggleSkillVisibility(ag.ID, "t-skill", true); err != nil {
		t.Fatal(err)
	}
	// agent-side read
	vm, _ := d.AgentVisible(ag.ID, "mcp")
	if len(vm) != 1 || vm[0] != mid {
		t.Fatalf("agent visible mcp: %+v", vm)
	}
	// resource-side read (same join row) → bidirectional
	ra, _ := d.ResourceAgents("mcp", mid)
	if len(ra) != 1 || ra[0] != ag.ID {
		t.Fatalf("resource agents: %+v", ra)
	}
	// toggle off
	d.ToggleVisibility(ag.ID, "mcp", mid, false)
	vm2, _ := d.AgentVisible(ag.ID, "mcp")
	if len(vm2) != 0 {
		t.Fatalf("after toggle off: %+v", vm2)
	}
	// skill visibility is name-keyed: verify the read, then deleting the skill's
	// visibility rows (called when a skill is removed) clears it.
	names, _ := d.AgentSkillNames(ag.ID)
	if len(names) != 1 || names[0] != "t-skill" {
		t.Fatalf("agent visible skills: %+v", names)
	}
	if err := d.DeleteSkillVisibility("t-skill"); err != nil {
		t.Fatal(err)
	}
	names2, _ := d.AgentSkillNames(ag.ID)
	if len(names2) != 0 {
		t.Fatalf("skill visibility should be cleared on delete: %+v", names2)
	}
	d.DeleteMCP(mid)
}

func TestSeedHeadedBrowserDefaults(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	findBrowser := func() *MCPServer {
		servers, err := d.ListMCP()
		if err != nil {
			t.Fatal(err)
		}
		for _, server := range servers {
			if server.Name == "browser" {
				copy := *server
				copy.Args = append(json.RawMessage(nil), server.Args...)
				copy.Env = append(json.RawMessage(nil), server.Env...)
				return &copy
			}
		}
		return nil
	}
	beforeBrowser := findBrowser()
	beforeSetting, hadSetting, err := d.GetSetting("headed_browser_defaults_v2")
	if err != nil {
		t.Fatal(err)
	}
	agents := make([]*Agent, 0, 2)
	beforeSkills := map[int64][]string{}
	for _, key := range []string{"pentest", "worker"} {
		a, err := d.GetAgentByKey(key)
		if err != nil || a == nil {
			t.Fatalf("agent %s: %v", key, err)
		}
		agents = append(agents, a)
		beforeSkills[a.ID], _ = d.AgentSkillNames(a.ID)
	}
	t.Cleanup(func() {
		if beforeBrowser == nil {
			if current := findBrowser(); current != nil {
				_ = d.DeleteMCP(current.ID)
			}
		} else {
			_, _ = d.SaveMCP(beforeBrowser)
		}
		for _, a := range agents {
			_ = d.SetAgentSkillVisibility(a.ID, beforeSkills[a.ID])
		}
		if hadSetting {
			_ = d.SetSetting("headed_browser_defaults_v2", beforeSetting)
		} else {
			_, _ = d.Exec(`DELETE FROM settings WHERE key='headed_browser_defaults_v2'`)
		}
	})

	browser := beforeBrowser
	if browser == nil {
		browser = &MCPServer{Name: "browser"}
	}
	browser.Transport, browser.Command = "stdio", "npx"
	browser.Args, browser.Env = json.RawMessage(`["@playwright/mcp","--headless","--proxy-server","http://127.0.0.1:8788","--proxy-bypass","localhost"]`), json.RawMessage(`{}`)
	if _, err := d.SaveMCP(browser); err != nil {
		t.Fatal(err)
	}
	_, _ = d.Exec(`DELETE FROM settings WHERE key='headed_browser_defaults_v2'`)
	for _, a := range agents {
		if err := d.ToggleSkillVisibility(a.ID, "playwright-cli", false); err != nil {
			t.Fatal(err)
		}
	}

	if err := d.seedHeadedBrowserDefaults(); err != nil {
		t.Fatal(err)
	}
	var args []string
	if current := findBrowser(); current == nil {
		t.Fatal("browser MCP was not seeded")
	} else if err := json.Unmarshal(current.Args, &args); err != nil ||
		current.Command != "playwright-mcp" || len(args) != 2 ||
		args[0] != "--idle-timeout" || args[1] != "300000" {
		t.Fatalf("browser command=%q args=%s err=%v, want headed managed default", current.Command, current.Args, err)
	}
	for _, a := range agents {
		names, _ := d.AgentSkillNames(a.ID)
		if !slices.Contains(names, "playwright-cli") {
			t.Fatalf("agent %s missing playwright-cli: %v", a.Key, names)
		}
	}

	current := findBrowser()
	current.Args = json.RawMessage(`["user-custom-browser"]`)
	if _, err := d.SaveMCP(current); err != nil {
		t.Fatal(err)
	}
	_, _ = d.Exec(`DELETE FROM settings WHERE key='headed_browser_defaults_v2'`)
	if err := d.seedHeadedBrowserDefaults(); err != nil {
		t.Fatal(err)
	}
	if current = findBrowser(); string(current.Args) != `["user-custom-browser"]` {
		t.Fatalf("migration overwrote custom browser args: %s", current.Args)
	}
	if err := d.ToggleSkillVisibility(agents[1].ID, "playwright-cli", false); err != nil {
		t.Fatal(err)
	}
	if err := d.seedHeadedBrowserDefaults(); err != nil {
		t.Fatal(err)
	}
	if names, _ := d.AgentSkillNames(agents[1].ID); slices.Contains(names, "playwright-cli") {
		t.Fatalf("versioned seed resurrected disabled worker skill: %v", names)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
