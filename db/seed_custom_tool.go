package db

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// testLoginIdentityJSON is the shipped definition of the test_login_identity
// script tool: disposable login email + a pinned geonode sticky egress session.
// See db/tools.go SeedCustomTool for the first-insert-only policy.
//
//go:embed test_login_identity.json
var testLoginIdentityJSON []byte

// SeedTestLoginIdentity inserts the custom test_login_identity tool ONCE on a
// fresh database. Gated by a settings flag and ON CONFLICT DO NOTHING, so a user
// editing (or deleting) the tool in the UI is never overwritten or resurrected on
// restart — same policy as SeedTool for built-ins.
func (d *DB) SeedTestLoginIdentity() error {
	const flag = "test_login_identity_tool_v1"
	if v, _, _ := d.GetSetting(flag); v == "true" {
		return nil
	}
	var t Tool
	if err := json.Unmarshal(testLoginIdentityJSON, &t); err != nil {
		return fmt.Errorf("test_login_identity seed: %w", err)
	}
	agents, err := json.Marshal(t.Agents)
	if err != nil {
		return fmt.Errorf("test_login_identity seed: %w", err)
	}
	if err := d.SeedCustomTool(t.Key, t.Description, t.Schema, agents, t.Kind, t.Exec, t.Deferred); err != nil {
		return fmt.Errorf("test_login_identity seed: %w", err)
	}
	return d.SetSetting(flag, "true")
}
