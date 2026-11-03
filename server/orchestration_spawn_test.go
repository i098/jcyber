package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestSpawnTaskPreservesNamedTarget(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	s := newAdmissionTestServer(m, func(*Task) bool { return false })
	host := fmt.Sprintf("spawn-%d.example.test", time.Now().UnixNano())
	name := host + ":8080 audit"
	in, err := json.Marshal(map[string]string{
		"name": name, "description": "Use https://docs.vendor.net as reference", "goal": "Find exposed admin routes",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.toolSpawnTask().Call(context.Background(), in, nil)
	if err != nil || result.IsError {
		t.Fatalf("spawn_task: err=%v result=%+v", err, result)
	}
	var spawned *Task
	for _, task := range m.List() {
		if task.Name == name {
			spawned = task
			break
		}
	}
	if spawned == nil {
		t.Fatalf("spawn_task did not create task named %q", name)
	}
	defer func() { _, _ = m.DeleteTask(spawned.ID, DeleteTaskOptions{DeleteAssets: true}) }()
	if got := taskCreationName(spawned); got != name {
		t.Fatalf("creation name = %q, want %q", got, name)
	}
	assets, err := m.Assets().QueryByTask(mustTaskID(t, spawned.ID), "", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "http://" + host + ":8080"
	for _, asset := range assets {
		if asset.URL == wantURL {
			return
		}
	}
	t.Fatalf("seeded assets = %+v, want %q", assets, wantURL)
}

func TestSpawnTaskSeedsGoalTargetWithoutNameOrDescriptionReference(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	s := newAdmissionTestServer(m, func(*Task) bool { return false })
	host := fmt.Sprintf("goal-%d.example.test", time.Now().UnixNano())
	goal := "Map http://" + host
	in, err := json.Marshal(map[string]string{
		"description": "Use https://docs.vendor.net as reference", "goal": goal,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.toolSpawnTask().Call(context.Background(), in, nil)
	if err != nil || result.IsError {
		t.Fatalf("spawn_task: err=%v result=%+v", err, result)
	}
	var spawned *Task
	for _, task := range m.List() {
		if task.Goal == goal {
			spawned = task
			break
		}
	}
	if spawned == nil {
		t.Fatalf("spawn_task did not create task with goal %q", goal)
	}
	defer func() { _, _ = m.DeleteTask(spawned.ID, DeleteTaskOptions{DeleteAssets: true}) }()
	if spawned.Name != "" || taskCreationName(spawned) != "" {
		t.Fatalf("spawn_task assigned a name when omitted: %q", spawned.Name)
	}
	assets, err := m.Assets().QueryByTask(mustTaskID(t, spawned.ID), "", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	goalFound := false
	for _, asset := range assets {
		if asset.URL == "http://"+host {
			goalFound = true
		}
		if asset.URL == "https://docs.vendor.net" {
			t.Fatalf("description reference seeded as target: %+v", assets)
		}
	}
	if !goalFound {
		t.Fatalf("goal target missing from seeded assets: %+v", assets)
	}
}

func TestSpawnSchemaRefreshPreservesSavedEdits(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { m.Close() })
	s := &Server{m: m}
	tool := s.toolSpawnTask()
	schema, err := json.Marshal(tool.InputSchema())
	if err != nil {
		t.Fatal(err)
	}
	before, err := m.pg.GetTool(tool.Name())
	if err != nil {
		t.Fatal(err)
	}
	existed := before != nil
	if !existed {
		if err := m.pg.SeedTool(tool.Name(), tool.Description(), schema, json.RawMessage(`[]`)); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Cleanup(func() {
			agents, _ := json.Marshal(before.Agents)
			_ = m.pg.UpdateTool(tool.Name(), before.Description, before.Schema, agents, before.Enabled)
		})
	}
	if !existed {
		t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM tools WHERE key=$1`, tool.Name()) })
	}
	const flag = "tool_schema_refresh_v11_spawn_optional_name"
	previous, present, err := m.pg.GetSetting(flag)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = m.pg.SetSetting(flag, previous)
		} else {
			_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, flag)
		}
	})
	custom := json.RawMessage(`{"type":"object","properties":{"description":{"type":"string","description":"saved context","default":"keep me"},"goal":{"type":"string","description":"saved goal"},"timeout_seconds":{"type":"integer","default":23}},"required":["name","description","goal"]}`)
	if err := m.pg.UpdateTool(tool.Name(), "saved spawn instructions", custom, json.RawMessage(`["worker"]`), false); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSetting(flag, "false"); err != nil {
		t.Fatal(err)
	}
	s.refreshSpawnTaskSchema()
	got, err := m.pg.GetTool(tool.Name())
	if err != nil || got == nil {
		t.Fatalf("spawn_task missing after refresh: %v", err)
	}
	if got.Description != "saved spawn instructions" || got.Enabled || len(got.Agents) != 1 || got.Agents[0] != "worker" {
		t.Fatalf("saved tool settings changed: %+v", got)
	}
	type schemaState struct {
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
	}
	var parsed schemaState
	if err := json.Unmarshal(got.Schema, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Properties["description"]["description"] != "saved context" || parsed.Properties["description"]["default"] != "keep me" || parsed.Properties["goal"]["description"] != "saved goal" || parsed.Properties["timeout_seconds"]["default"] != float64(23) {
		t.Fatalf("saved parameter edits changed: %+v", parsed.Properties)
	}
	if parsed.Properties["name"]["type"] != "string" {
		t.Fatalf("missing name parameter: %+v", parsed.Properties["name"])
	}
	efforts, ok := parsed.Properties["thinking_effort"]["enum"].([]any)
	if !ok || len(efforts) != 2 || efforts[0] != ThinkingEffortMax || efforts[1] != ThinkingEffortAuto {
		t.Fatalf("missing thinking effort options: %v", parsed.Properties["thinking_effort"])
	}
	names := 0
	for _, required := range parsed.Required {
		if required == "name" {
			names++
		}
	}
	if names != 0 {
		t.Fatalf("required name count = %d, want 0", names)
	}
	legacy := json.RawMessage(`{"type":"object","properties":{"description":{"type":"string","description":"Task description (short title)","default":"keep me"},"goal":{"type":"string"}},"required":["description","goal"]}`)
	if err := m.pg.UpdateTool(tool.Name(), "saved spawn instructions", legacy, json.RawMessage(`["worker"]`), false); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSetting(flag, "false"); err != nil {
		t.Fatal(err)
	}
	s.refreshSpawnTaskSchema()
	got, err = m.pg.GetTool(tool.Name())
	if err != nil || got == nil {
		t.Fatalf("spawn_task missing after legacy refresh: %v", err)
	}
	parsed = schemaState{}
	if err := json.Unmarshal(got.Schema, &parsed); err != nil {
		t.Fatal(err)
	}
	wantDescription := tool.InputSchema()["properties"].(map[string]any)["description"].(map[string]any)["description"]
	if parsed.Properties["description"]["description"] != wantDescription || parsed.Properties["description"]["default"] != "keep me" || got.Description != "saved spawn instructions" {
		t.Fatalf("legacy description upgrade changed user edits: %+v", got)
	}
}
