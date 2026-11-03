package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestSpawnTaskRequiresName(t *testing.T) {
	s := &Server{}
	in := json.RawMessage(`{"description":"Scan target","goal":"Find exposed admin routes"}`)
	result, err := s.toolSpawnTask().Call(context.Background(), in, nil)
	if err == nil && !result.IsError {
		t.Fatalf("spawn_task accepted a task without a name: %+v", result)
	}
}

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
