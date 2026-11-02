package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"normasdk/llm"
)

// TestCodexAccessProgramsWire locks the Daybreak routing: a chatgpt.com Codex
// base URL resolves access_programs={"cyber":"daybreak_blue"}, the env override
// and the off switch work, other endpoints stay clean, and the selector really
// reaches the request body the Responses provider sends.
func TestCodexAccessProgramsWire(t *testing.T) {
	const codexBase = "https://chatgpt.com/backend-api/codex"

	c := ConfigFrom("openai-responses", "gpt-6-sol", codexBase, "tok", "")
	if got := c.AccessProgram; got != "daybreak_blue" {
		t.Fatalf("default program = %q, want daybreak_blue", got)
	}
	if c.ServiceTier != "priority" {
		t.Fatalf("service_tier = %q, want priority on the codex endpoint", c.ServiceTier)
	}
	if c.BaseURL != codexBase {
		t.Fatalf("base_url = %q, want %q", c.BaseURL, codexBase)
	}

	t.Setenv("JCYBER_CODEX_ACCESS_PROGRAM", "daybreak_red")
	if got := ConfigFrom("openai-responses", "m", codexBase, "k", "").AccessProgram; got != "daybreak_red" {
		t.Fatalf("override program = %q, want daybreak_red", got)
	}
	t.Setenv("JCYBER_CODEX_SERVICE_TIER", "off")
	if st := ConfigFrom("openai-responses", "m", codexBase, "k", "").ServiceTier; st != "" {
		t.Fatalf("tier off switch sent %q, want empty", st)
	}
	t.Setenv("JCYBER_CODEX_SERVICE_TIER", "")
	t.Setenv("JCYBER_CODEX_ACCESS_PROGRAM", "off")
	if p := ConfigFrom("openai-responses", "m", codexBase, "k", "").AccessProgram; p != "" {
		t.Fatalf("off switch sent %q, want empty", p)
	}
	t.Setenv("JCYBER_CODEX_ACCESS_PROGRAM", "")

	if p := ConfigFrom("openai-responses", "gpt-5", "https://gw.example/v1", "k", "").AccessProgram; p != "" {
		t.Fatalf("non-codex endpoint sent %q, want empty", p)
	}
	if st := ConfigFrom("openai-responses", "gpt-5", "https://gw.example/v1", "k", "").ServiceTier; st != "" {
		t.Fatalf("non-codex endpoint sent tier %q, want empty", st)
	}

	// Wire check: the same body the Codex backend sees.
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}))
	defer srv.Close()

	c.BaseURL = srv.URL
	prov, err := c.NewProvider()
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	for ev, err := range prov.Stream(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockText, Text: "hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		_ = ev
	}
	if raw, ok := got["access_programs"].(map[string]any); !ok || raw["cyber"] != "daybreak_blue" {
		t.Fatalf("request body access_programs = %v, want {\"cyber\":\"daybreak_blue\"} (body keys: %v)", got["access_programs"], keysOf(got))
	}
	if got["service_tier"] != "priority" {
		t.Fatalf("request body service_tier = %v, want priority", got["service_tier"])
	}
	if got["store"] != false {
		t.Fatalf("store = %v, want false", got["store"])
	}
}

func keysOf(m map[string]any) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return strings.Join(ks, ",")
}
