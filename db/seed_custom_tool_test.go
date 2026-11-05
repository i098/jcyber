package db

import (
	"strings"
	"testing"
)

// TestSeedTestLoginIdentity covers the shipped custom tool seed: it inserts once,
// keeps the row user-editable (system=false), survives a restart without
// overwriting what the user changed, and leaves a deleted row deleted.
func TestSeedTestLoginIdentity(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	const key, flag = "test_login_identity", "test_login_identity_tool_v1"
	reset := func() {
		_ = d.DeleteCustomTool(key)
		_, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, flag)
		_ = d.DeleteCustomTool("gmail_read")
		_, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, "gmail_read_tool_v1")
	}
	reset()
	defer reset()

	if err := d.SeedCustomTools(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	gm, err := d.GetTool("gmail_read")
	if err != nil || gm == nil {
		t.Fatalf("gmail_read after seed: %v %v", gm, err)
	}
	if gm.System || gm.Kind != "script" || len(gm.Exec) == 0 {
		t.Fatalf("gmail_read seeded wrong: system=%v kind=%q exec=%d", gm.System, gm.Kind, len(gm.Exec))
	}
	got, err := d.GetTool(key)
	if err != nil || got == nil {
		t.Fatalf("tool after seed: %v %v", got, err)
	}
	if got.System {
		t.Fatal("seeded custom tool must be user-editable (system=false)")
	}
	if got.Kind != "script" || len(got.Exec) == 0 {
		t.Fatalf("kind=%q exec=%d bytes, want script with an exec spec", got.Kind, len(got.Exec))
	}
	if !strings.Contains(string(got.Schema), "sticky_minutes") {
		t.Fatalf("schema is missing sticky_minutes: %s", got.Schema)
	}
	if !strings.Contains(string(got.Exec), "proxy.geonode.io:10000") {
		t.Fatal("exec does not pin a geonode sticky session")
	}

	// A user edit must survive the next startup (flag cleared, seed re-runs).
	if err := d.UpdateCustomTool(&Tool{
		Key: key, Description: "edited in the UI", Schema: got.Schema, Agents: got.Agents,
		Enabled: true, Kind: got.Kind, Exec: got.Exec,
	}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	_, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, flag)
	if err := d.SeedCustomTools(); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	got, _ = d.GetTool(key)
	if got.Description != "edited in the UI" {
		t.Fatalf("re-seed overwrote the user's description: %q", got.Description)
	}

	// Deleting the tool and re-seeding must not resurrect it on the same DB —
	// the versioned flag is the one-time gate.
	if err := d.DeleteCustomTool(key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := d.SeedCustomTools(); err != nil {
		t.Fatalf("seed after delete: %v", err)
	}
	got, _ = d.GetTool(key)
	if got != nil {
		t.Fatal("re-seed resurrected a deleted tool; the settings flag should gate it")
	}
}
