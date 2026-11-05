package db

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// Shipped custom tools: definitions live next to this file, so a fresh install
// gets them without an import step. Each is inserted ONCE (versioned settings
// flag + ON CONFLICT DO NOTHING), which keeps later UI edits, disables and
// deletions intact across restarts — same policy as SeedTool for built-ins.
//
//go:embed test_login_identity.json
var testLoginIdentityJSON []byte

//go:embed gmail_read.json
var gmailReadJSON []byte

var shippedCustomTools = []struct {
	flag string
	data []byte
}{
	{"test_login_identity_tool_v1", testLoginIdentityJSON},
	{"gmail_read_tool_v1", gmailReadJSON},
}

// SeedCustomTools installs every shipped custom tool that has not been seeded yet.
func (d *DB) SeedCustomTools() error {
	for _, st := range shippedCustomTools {
		if v, _, _ := d.GetSetting(st.flag); v == "true" {
			continue
		}
		var t Tool
		if err := json.Unmarshal(st.data, &t); err != nil {
			return fmt.Errorf("%s seed: %w", st.flag, err)
		}
		agents, err := json.Marshal(t.Agents)
		if err != nil {
			return fmt.Errorf("%s seed: %w", t.Key, err)
		}
		if err := d.SeedCustomTool(t.Key, t.Description, t.Schema, agents, t.Kind, t.Exec, t.Deferred); err != nil {
			return fmt.Errorf("%s seed: %w", t.Key, err)
		}
		if err := d.SetSetting(st.flag, "true"); err != nil {
			return fmt.Errorf("%s seed: %w", t.Key, err)
		}
	}
	return nil
}
