package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateTaskRequiresName(t *testing.T) {
	for _, name := range []string{"", "   "} {
		body, err := json.Marshal(map[string]string{
			"name": name, "description": "Test example.com", "goal": "Scan target",
		})
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		(&Server{}).createTask(recorder, httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(body)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("name %q: status %d, want 400", name, recorder.Code)
		}
		var response struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.Error != "name is required" {
			t.Fatalf("name %q: error %q", name, response.Error)
		}
	}
}
