package core

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/keychain"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/window"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

// fakeOpenAI is a chat-completions server that answers with its replies in
// order and keeps the requests it got.
type fakeOpenAI struct {
	*httptest.Server
	mu       sync.Mutex
	replies  []string
	requests []seenRequest
}

type seenRequest struct{ body, auth string }

// oaiText and oaiCall are assistant messages in the protocol's JSON.
func oaiText(s string) string { return `{"role":"assistant","content":` + quote(s) + `}` }
func oaiCall(id, name, args string) string {
	return `{"role":"assistant","content":"","tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"` + name + `","arguments":` + quote(args) + `}}]}`
}
func quote(s string) string { b, _ := jsonMarshal(s); return string(b) }

func newFakeOpenAI(t *testing.T, replies ...string) *fakeOpenAI {
	f := &fakeOpenAI{replies: replies}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, seenRequest{string(body), r.Header.Get("Authorization")})
		if len(f.replies) == 0 {
			http.Error(w, `{"error":"script exhausted"}`, http.StatusInternalServerError)
			return
		}
		msg := f.replies[0]
		f.replies = f.replies[1:]
		w.Write([]byte(`{"choices":[{"message":` + msg + `,"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeOpenAI) seen() []seenRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seenRequest(nil), f.requests...)
}

// modelRig is a session with init.scm and a model registry.
func modelRig(t *testing.T, init string, models map[string]config.Model, o Options, turns ...fake.Turn) *rig {
	t.Helper()
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".nibb"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nibb", "init.scm"), []byte(init), 0o644)
	srv := fake.New(turns...)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Models = models
	logb := &bytes.Buffer{}
	o.Config, o.Repo, o.Provider, o.LogWriter, o.AutoApprove = cfg, repo, llm.NewOllama(srv.URL), logb, true
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: repo, logb: logb}
}

// replayAll proves every recorded model request can be rebuilt from the log,
// whichever model it went to.
func replayAll(t *testing.T, logb *bytes.Buffer) int {
	t.Helper()
	evs, err := log.Read(bytes.NewReader(logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Kind == log.KindLLMReq {
			if _, err := window.ReplayRequest(evs, ev.Seq); err != nil {
				t.Fatalf("replay of seq %d: %v", ev.Seq, err)
			}
			n++
		}
	}
	return n
}

func TestModelTurnByName(t *testing.T) {
	impl := newFakeOpenAI(t, oaiCall("c1", "fs-list", `{"path":"."}`), oaiText("listed"))
	r := modelRig(t, `
(define (agent-turn request)
  (let loop ((reply (model-turn "impl")))
    (unless (null? (assoc-ref reply 'tool-calls '()))
      (run-tools (assoc-ref reply 'tool-calls))
      (loop (model-turn "impl")))))`,
		map[string]config.Model{"impl": {Provider: "openai", URL: impl.URL + "/v1", Model: "impl-model"}}, Options{})
	if err := r.turn("list files"); err != "" {
		t.Fatal(err)
	}
	seen := impl.seen()
	if len(seen) != 2 || len(r.srv.Requests) != 0 {
		t.Fatalf("impl got %d requests, control %d", len(seen), len(r.srv.Requests))
	}
	if !strings.Contains(seen[1].body, `"tool_call_id":"c1"`) {
		t.Fatalf("the tool result did not reach the model that asked:\n%s", seen[1].body)
	}
	if !strings.Contains(r.logb.String(), `"endpoint":"impl"`) {
		t.Fatal("llm.req does not record the model's name")
	}
	if n := replayAll(t, r.logb); n != 2 {
		t.Fatalf("replayed %d requests", n)
	}
}

func TestModelTurnUnknownName(t *testing.T) {
	r := modelRig(t, `(define (agent-turn request) (model-turn "gpt-9"))`,
		map[string]config.Model{"impl": {Provider: "ollama", URL: "http://127.0.0.1:1", Model: "m"}}, Options{})
	if err := r.turn("hi"); !strings.Contains(err, `no model "gpt-9" is configured; [models] has: impl`) {
		t.Fatalf("turn error %q", err)
	}
}

// A keyed model gets its key only where the key is bound, like any other
// credential; the check is logged and the key never is.
func TestKeyedModelNeedsItsBinding(t *testing.T) {
	const secret = "sk-model-canary-0042"
	for _, bound := range []bool{false, true} {
		impl := newFakeOpenAI(t, oaiText("hello"))
		kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
		if err != nil {
			t.Fatal(err)
		}
		kc.Add("impl-key", []byte(secret))
		var table binding.Table
		if bound {
			table.Bind = []binding.Binding{{Class: "impl-key", Dest: "127.0.0.1", Positions: []string{"header:Authorization:Bearer"}, Methods: []string{"POST"}}}
		}
		r := modelRig(t, `(define (agent-turn request) (model-turn "impl"))`,
			map[string]config.Model{"impl": {Provider: "openai", URL: impl.URL, Model: "m", Credential: "impl-key"}},
			Options{Keychain: kc, Bindings: table})
		err2 := r.turn("hi")
		seen := impl.seen()
		switch {
		case !bound && (!strings.Contains(err2, "binding") || len(seen) != 0):
			t.Fatalf("unbound: error %q, %d requests sent", err2, len(seen))
		case bound && (err2 != "" || len(seen) != 1 || seen[0].auth != "Bearer "+secret):
			t.Fatalf("bound: error %q, requests %+v", err2, seen)
		}
		if !strings.Contains(r.logb.String(), `"kind":"egress.check"`) {
			t.Fatal("the key's binding check was not logged")
		}
		if strings.Contains(r.logb.String(), secret) {
			t.Fatal("the model's key reached the log")
		}
	}
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
