package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCreateTaskWithoutNameSeedsGoal(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	s := newAdmissionTestServer(m, func(*Task) bool { return false })
	host := fmt.Sprintf("unnamed-%d.example.test", time.Now().UnixNano())
	body, err := json.Marshal(map[string]string{
		"description": "Use https://docs.vendor.net as reference",
		"goal":        "Map http://" + host,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	s.createTask(recorder, httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(body)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create unnamed task: %d %s", recorder.Code, recorder.Body.String())
	}
	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "" {
		t.Fatalf("unnamed task = %+v", created)
	}
	defer func() { _, _ = m.DeleteTask(created.ID, DeleteTaskOptions{DeleteAssets: true}) }()
	assets, err := m.Assets().QueryByTask(mustTaskID(t, created.ID), "", 20, 0)
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
