package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestTaskMetadataPatchReturnsRenameAndPin(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("metadata patch", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	defer func() { _ = m.pg.DeleteTask(taskID) }()
	s := New(context.Background(), m, t.TempDir(), t.TempDir(), t.TempDir())
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"name": "  renamed task  ", "pinned": true})
	req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+task.ID, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	var updated struct {
		Name     string `json:"name"`
		Pinned   bool   `json:"pinned"`
		PinnedAt string `json:"pinned_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed task" || !updated.Pinned || updated.PinnedAt == "" {
		t.Fatalf("unexpected task patch response: %+v", updated)
	}

	body, _ = json.Marshal(map[string]any{"name": strings.Repeat("任", maxTaskNameRunes+1)})
	req = httptest.NewRequest(http.MethodPatch, "/api/tasks/"+task.ID, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("overlong task name status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTaskRename(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("original description", "original goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	defer func() { _ = m.pg.DeleteTask(taskID) }()
	s := New(context.Background(), m, t.TempDir(), t.TempDir(), t.TempDir())
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()

	for _, test := range []struct {
		label  string
		id     string
		name   string
		status int
	}{
		{"trimmed name persists", task.ID, " \tRenamed task\n ", http.StatusOK},
		{"empty name rejected", task.ID, "", http.StatusBadRequest},
		{"whitespace name rejected", task.ID, " \t\n ", http.StatusBadRequest},
		{"unknown task rejected", "4611686018427387903", "Missing task", http.StatusNotFound},
	} {
		t.Run(test.label, func(t *testing.T) {
			body, err := json.Marshal(map[string]string{"name": test.name})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+test.id, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != test.status {
				t.Fatalf("rename status=%d want=%d body=%s", rec.Code, test.status, rec.Body.String())
			}
			if test.status == http.StatusOK {
				var updated struct {
					Name string `json:"name"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
					t.Fatal(err)
				}
				if updated.Name != "Renamed task" {
					t.Fatalf("rename response name=%q", updated.Name)
				}
			}
			persisted, err := m.pg.GetTask(taskID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted == nil || persisted.Name != "Renamed task" {
				t.Fatalf("rename did not persist or invalid request changed name: %+v", persisted)
			}
			if persisted.Description != "original description" || persisted.Goal != "original goal" {
				t.Fatalf("rename changed description or goal: %+v", persisted)
			}
		})
	}
}

func TestConversationBatchDeleteReportsMissing(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	s := New(context.Background(), m, t.TempDir(), t.TempDir(), t.TempDir())
	first, err := m.pg.CreateConversation("mainagent", "batch-http-first", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.pg.CreateConversation("mainagent", "batch-http-second", nil)
	if err != nil {
		_ = m.pg.DeleteConversation(first.ID)
		t.Fatal(err)
	}
	defer func() {
		_ = m.pg.DeleteConversation(first.ID)
		_ = m.pg.DeleteConversation(second.ID)
	}()
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	missing := int64(1<<62 - 1)
	body, _ := json.Marshal(map[string]any{"ids": []int64{first.ID, missing, second.ID}})
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/delete/batch", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Items []conversationDeleteItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 3 || !response.Items[0].OK || response.Items[1].OK || !response.Items[2].OK {
		t.Fatalf("batch delete response=%+v", response.Items)
	}
	for _, id := range []int64{first.ID, second.ID} {
		if conversation, err := m.pg.GetConversation(id); err != nil || conversation != nil {
			t.Fatalf("conversation %d still exists: %+v err=%v", id, conversation, err)
		}
	}
	if response.Items[1].Error == "" {
		t.Fatalf("missing conversation %s must include an error", fmt.Sprint(missing))
	}
}
