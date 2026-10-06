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
	"github.com/fclaude/nodwyn/internal/log"
)

// The compaction request can itself need trimming. A server that validates
// tool history must still receive complete call/result pairs at that point.
func TestCerebrasCompactsAfterTrimmingToolCalls(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role  string `json:"role"`
				ID    string `json:"tool_call_id"`
				Calls []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "invalid JSON", 400)
			return
		}
		pending := map[string]bool{}
		for _, m := range req.Messages {
			if m.Role == "tool" {
				if !pending[m.ID] {
					http.Error(w, "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'", 400)
					return
				}
				delete(pending, m.ID)
			} else {
				if len(pending) > 0 {
					http.Error(w, "unanswered tool calls", 400)
					return
				}
				for _, c := range m.Calls {
					pending[c.ID] = true
				}
			}
		}
		if len(pending) > 0 {
			http.Error(w, "unanswered tool calls", 400)
			return
		}
		n := calls.Add(1)
		msg := map[string]any{"role": "assistant", "content": "The files were listed; continue from this handover."}
		if n <= 3 {
			msg["content"] = ""
			msg["tool_calls"] = []any{map[string]any{"id": fmt.Sprint(n), "type": "function", "function": map[string]any{"name": "fs-list", "arguments": `{"path":"."}`}}}
			if n == 1 {
				msg["reasoning"] = strings.Repeat("thinking ", 5000)
			}
		}
		if n > 4 {
			t.Errorf("script exhausted: %d", n)
			http.Error(w, "script exhausted", 400)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": "stop"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	cfg, err := config.Parse([]byte(fmt.Sprintf(`[control]
provider = "openai"
dialect = "cerebras"
url = %q
model = "fake"
num_ctx = 24096
[window]
high_water = 20000
low_water = 12000
max_result_tokens = 1000
output_reserve = 4096
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".nodwyn"), 0o755); err != nil {
		t.Fatal(err)
	}
	init := `(define (agent-turn request)
  (let loop ((left 3))
    (when (> left 0)
      (run-tools (assoc-ref (model-turn) 'tool-calls '()))
      (loop (- left 1))))
  (compact-context request))`
	if err := os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(init), 0o644); err != nil {
		t.Fatal(err)
	}
	logb := &bytes.Buffer{}
	s, err := newInitSession(Options{Config: cfg, Repo: repo, LogWriter: logb, NoConfirm: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := (&rig{t: t, s: s}).turn("list, then compact"); err != "" {
		t.Fatal(err)
	}
	trimmed, compacted := false, false
	for _, ev := range s.EventsSoFar() {
		if ev.Kind == log.KindLLMReq {
			var p log.LLMReq
			if err := ev.Decode(&p); err != nil {
				t.Fatal(err)
			}
			trimmed = trimmed || len(p.Elided) > 0
		}
		if ev.Kind == log.KindContextCompact {
			compacted = true
		}
	}
	if !trimmed || !compacted || calls.Load() != 4 {
		t.Fatalf("trimmed=%v compacted=%v calls=%d", trimmed, compacted, calls.Load())
	}
	if n := replayAll(t, logb); n != 4 {
		t.Fatalf("replayed %d requests", n)
	}
}
