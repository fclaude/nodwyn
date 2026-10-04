package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
)

// The configured dialect reaches every model-selection path, preserves the
// actual tool result on the next request, and leaves log replay canonical.
func TestCerebrasDialectThroughTheLoop(t *testing.T) {
	for _, tc := range []struct {
		name   string
		choice ModelChoice
		call   string
	}{
		{"control", ModelChoice{}, `(model-turn)`},
		{"in-memory server", ModelChoice{}, `(model-turn)`},
		{"switch", ModelChoice{}, `(model-turn)`},
		{"server choice", ModelChoice{Server: "fast", Model: "m"}, `(model-turn)`},
		{"named choice", ModelChoice{Name: "worker"}, `(model-turn)`},
		{"named turn", ModelChoice{}, `(model-turn "worker")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "GET" && req.URL.Path == "/v1/models" {
					if _, err := w.Write([]byte(`{"data":[{"id":"m","context_length":131072},{"id":"other","context_length":131072}]}`)); err != nil {
						t.Error(err)
					}
					return
				}
				if req.Method != "POST" || req.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
					http.Error(w, "unexpected request", 400)
					return
				}
				n := calls.Add(1)
				var body struct {
					Model    string                       `json:"model"`
					Messages []map[string]json.RawMessage `json:"messages"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
					http.Error(w, "invalid JSON", 400)
					return
				}
				if n == 3 && tc.name == "switch" && body.Model != "other" {
					t.Errorf("switch asked %q", body.Model)
				}
				thinking, result := false, false
				for _, m := range body.Messages {
					if _, bad := m["reasoning_content"]; bad {
						http.Error(w, "reasoning_content is unsupported", 400)
						return
					}
					if string(m["reasoning"]) == `"inspect first"` {
						thinking = true
					}
					if string(m["tool_call_id"]) == `"c1"` && strings.Contains(string(m["content"]), "marker.txt") {
						result = true
					}
				}
				if n > 1 && (!thinking || !result) {
					http.Error(w, "missing reasoning or tool result", 400)
					return
				}
				msg := `{"role":"assistant","content":"done"}`
				if n == 1 {
					msg = `{"role":"assistant","content":null,"reasoning":"inspect first","tool_calls":[{"id":"c1","type":"function","function":{"name":"fs-list","arguments":"{\"path\":\".\"}"}}]}`
				}
				if n > 3 {
					t.Errorf("script exhausted after %d requests", n)
					http.Error(w, "script exhausted", 400)
					return
				}
				if _, err := w.Write([]byte(`{"choices":[{"message":` + msg + `,"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			cfg, err := config.Parse([]byte(fmt.Sprintf(`[servers.fast]
provider = "openai"
dialect = "cerebras"
url = %q
[control]
server = "fast"
model = "m"
num_ctx = 131072
[models.worker]
server = "fast"
model = "m"
num_ctx = 131072
`, srv.URL+"/v1")))
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "in-memory server" {
				cfg.Control = config.Model{Server: "fast", Model: "m", NumCtx: 131072}
			}
			repo := t.TempDir()
			if err := os.Mkdir(filepath.Join(repo, ".nodwyn"), 0o755); err != nil {
				t.Fatal(err)
			}
			init := `(define (agent-turn request)
  (let loop ((reply ` + tc.call + `))
    (unless (null? (assoc-ref reply 'tool-calls '()))
      (run-tools (assoc-ref reply 'tool-calls))
      (loop ` + tc.call + `))))`
			if err := os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(init), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("listed"), 0o644); err != nil {
				t.Fatal(err)
			}
			logb := &bytes.Buffer{}
			s, err := New(Options{Config: cfg, Repo: repo, Model: tc.choice, LogWriter: logb, NoConfirm: true})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			rig := &rig{t: t, s: s}
			for i, ask := range []string{"list files", "what did you find?"} {
				if i == 1 && tc.name == "switch" {
					if _, err := s.SetModel(ModelChoice{Server: "fast", Model: "other"}); err != nil {
						t.Fatal(err)
					}
				}
				if err := rig.turn(ask); err != "" {
					t.Fatal(err)
				}
			}
			if calls.Load() != 3 {
				t.Fatalf("got %d requests, want 3", calls.Load())
			}
			if n := replayAll(t, logb); n != 3 {
				t.Fatalf("replayed %d requests", n)
			}
		})
	}
}
