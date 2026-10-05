package core

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/test/fakes/chat"
)

// fakeAnthropic is a Messages API server that answers with its replies in
// order, each a JSON array of content blocks, and keeps what it was sent.
type fakeAnthropic struct {
	*httptest.Server
	mu       sync.Mutex
	replies  []string
	requests []anthropicSeen
}

type anthropicSeen struct{ path, body, apiKey, authorization, version string }

// claudeText and claudeCall are the content of an assistant reply.
func claudeText(s string) string { return `[{"type":"text","text":` + quote(s) + `}]` }
func claudeCall(id, name, input string) string {
	return `[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}]`
}

func newFakeAnthropic(t *testing.T, replies ...string) *fakeAnthropic {
	f := &fakeAnthropic{replies: replies}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, anthropicSeen{r.URL.Path, string(body), r.Header.Get("x-api-key"), r.Header.Get("Authorization"), r.Header.Get("anthropic-version")})
		if len(f.replies) == 0 {
			http.Error(w, `{"type":"error","error":{"type":"api_error","message":"script exhausted"}}`, http.StatusInternalServerError)
			return
		}
		content := f.replies[0]
		f.replies = f.replies[1:]
		stop := "end_turn"
		if strings.Contains(content, `"tool_use"`) {
			stop = "tool_use"
		}
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":` + content + `,"stop_reason":"` + stop + `","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAnthropic) seen() []anthropicSeen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]anthropicSeen(nil), f.requests...)
}

// A model on Anthropic's Messages API is a model like any other: a tool call
// it makes is answered with a tool_result block of the next request, which
// carries the call's id, and every request replays from the log.
func TestModelTurnOnTheMessagesAPI(t *testing.T) {
	t.Parallel()
	impl := newFakeAnthropic(t, claudeCall("toolu_01", "fs-list", `{"path":"."}`), claudeText("listed"))
	r := modelRig(t, `
(define (agent-turn request)
  (let loop ((reply (model-turn "impl")))
    (unless (null? (assoc-ref reply 'tool-calls '()))
      (run-tools (assoc-ref reply 'tool-calls))
      (loop (model-turn "impl")))))`,
		map[string]config.Model{"impl": {Provider: "anthropic", URL: impl.URL + "/v1", Model: "claude-x"}}, Options{})
	if err := r.turn("list files"); err != "" {
		t.Fatal(err)
	}
	seen := impl.seen()
	if len(seen) != 2 || len(r.srv.Requests) != 0 {
		t.Fatalf("impl got %d requests, control %d", len(seen), len(r.srv.Requests))
	}
	if seen[0].path != "/v1/messages" || seen[0].version != "2023-06-01" || strings.Contains(seen[0].body, `"stream"`) {
		t.Fatalf("first request: %+v", seen[0])
	}
	if !strings.Contains(seen[1].body, `"type":"tool_use","id":"toolu_01"`) || !strings.Contains(seen[1].body, `"type":"tool_result","tool_use_id":"toolu_01"`) {
		t.Fatalf("the tool call and its result did not reach the model that asked:\n%s", seen[1].body)
	}
	if n := replayAll(t, r.logb); n != 2 {
		t.Fatalf("replayed %d requests", n)
	}
}

// A keyed model on Anthropic's API sends its key in x-api-key, and only where
// the key is bound for that position: a key bound as a bearer token, which is
// all a key for another provider has, is not sent, and nothing is.
func TestAnthropicKeyIsSentAsXAPIKeyWhereBound(t *testing.T) {
	t.Parallel()
	const secret = "sk-ant-model-canary-0042"
	for name, position := range map[string]string{"bound as x-api-key": "header:x-api-key", "bound as a bearer token": "header:Authorization:Bearer"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			impl := newFakeAnthropic(t, claudeText("hello"))
			kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := kc.Add("impl-key", []byte(secret)); err != nil {
				t.Fatal(err)
			}
			table := binding.Table{Bind: []binding.Binding{{Class: "impl-key", Dest: "127.0.0.1", Positions: []string{position}, Methods: []string{"POST"}}}}
			r := modelRig(t, `(define (agent-turn request) (model-turn "impl"))`,
				map[string]config.Model{"impl": {Provider: "anthropic", URL: impl.URL + "/v1", Model: "claude-x", Credential: "impl-key"}},
				Options{Keychain: kc, Bindings: table})
			e := r.turn("hi")
			seen := impl.seen()
			if position == "header:x-api-key" {
				if e != "" || len(seen) != 1 || seen[0].apiKey != secret || seen[0].authorization != "" {
					t.Fatalf("error %q, requests %+v", e, seen)
				}
			} else if !strings.Contains(e, "position header:x-api-key is not bound") || len(seen) != 0 {
				t.Fatalf("a bearer binding must not release an x-api-key: error %q, %d requests sent", e, len(seen))
			}
			if !strings.Contains(r.logb.String(), `"kind":"egress.check"`) {
				t.Fatal("the key's binding check was not logged")
			}
			if strings.Contains(r.logb.String(), secret) {
				t.Fatal("the model's key reached the log")
			}
		})
	}
}

// A conversation on a Claude model asks for what its entry says: its effort
// (with adaptive thinking, whose summary is the reply's thinking) and the
// ask to cache its prompt, which Claude reads back on the next request. The
// log has the thinking and the cached share of the prompt, and every request
// replays.
func TestConversationOnAnthropicAsksItsEffortAndCache(t *testing.T) {
	t.Parallel()
	const key = "sk-ant-conversation-key"
	srv := chat.New(key, chat.Model{ID: "claude-thinker", Efforts: []string{"low", "medium", "high"}})
	t.Cleanup(srv.Close)
	cfg, err := config.Parse([]byte(`
[servers.claude]
provider = "anthropic"
url = "` + srv.V1() + `"
credential = "model-key"

[control]
server = "claude"
model = "claude-thinker"
effort = "high"

[models.thinker]
server = "claude"
model = "claude-thinker"
efforts = ["low", "medium", "high"]
cache_control = "ephemeral"
`))
	if err != nil {
		t.Fatal(err)
	}
	kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kc.Add("model-key", []byte(key)); err != nil {
		t.Fatal(err)
	}
	rows := []binding.Binding{{Class: "model-key", Dest: "127.0.0.1", Positions: []string{"header:x-api-key"}, Methods: []string{"POST", "GET"}}}
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), NoConfirm: true, LogWriter: logb, Keychain: kc, Bindings: binding.Table{Bind: rows}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	// Claude does not cache a prompt this short (the fake counts a quarter of a
	// byte a token), so the first message is long.
	say(t, s, strings.Repeat("a long first message ", 400))
	say(t, s, "and again")
	var asked []chat.Request
	for _, req := range srv.Requests() {
		if req.Path == "/v1/messages" {
			asked = append(asked, req)
		}
	}
	if len(asked) != 2 {
		t.Fatalf("the provider saw %d messages requests: %+v", len(asked), srv.Requests())
	}
	for i, req := range asked {
		if !req.Authenticated || req.KeyHeader != "x-api-key" || req.Effort != "high" || !strings.Contains(req.Thinking, `"adaptive"`) || !strings.Contains(req.CacheControl, "ephemeral") || req.Answered != 200 {
			t.Fatalf("request %d: %+v", i, req)
		}
	}
	// The second prompt starts with the first, and Claude was asked to cache it.
	if asked[0].CachedTokens != 0 || asked[0].CacheWriteTokens == 0 || asked[1].CachedTokens == 0 {
		t.Fatalf("cache: first %+v, second %+v", asked[0], asked[1])
	}
	if !strings.Contains(logb.String(), "Thinking at high effort.") || !strings.Contains(logb.String(), `"cached_tokens"`) {
		t.Fatalf("the log has neither the reply's thinking nor its cached tokens:\n%.3000s", logb.String())
	}
	if strings.Contains(logb.String(), key) {
		t.Fatal("the key reached the log")
	}
	if n := replayAll(t, logb); n != 2 {
		t.Fatalf("replayed %d requests", n)
	}
}

// A window with no context length of its own is sized from what Anthropic's
// model list says (max_input_tokens), asked with the key where Anthropic reads
// it and after the same binding check; a key bound for POST alone leaves the
// window at its default and the note says why.
func TestAnthropicModelListSizesTheWindow(t *testing.T) {
	t.Parallel()
	const key = "sk-ant-list-key-0123456789"
	row := func(method string) binding.Binding {
		return binding.Binding{Class: "model-key", Dest: "127.0.0.1", Positions: []string{"header:x-api-key"}, Methods: []string{method}}
	}
	for _, tc := range []struct {
		name   string
		rows   []binding.Binding
		numCtx int
		note   string
	}{
		{"bound for its list", []binding.Binding{row("POST"), row("GET")}, 200000, "sized for claude-sized's context of 200000 tokens"},
		{"bound for POST alone", []binding.Binding{row("POST")}, 0, "method GET at position header:x-api-key is not bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := chat.New(key, chat.Model{ID: "claude-sized", Context: 200000})
			srv.ListNeedsKey = true
			t.Cleanup(srv.Close)
			cfg, err := config.Parse([]byte(`
[servers.claude]
provider = "anthropic"
url = "` + srv.V1() + `"
credential = "model-key"

[control]
server = "claude"
model = "claude-sized"
`))
			if err != nil {
				t.Fatal(err)
			}
			kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := kc.Add("model-key", []byte(key)); err != nil {
				t.Fatal(err)
			}
			logb := &bytes.Buffer{}
			s, err := New(Options{Config: cfg, Repo: t.TempDir(), NoConfirm: true, LogWriter: logb, Keychain: kc, Bindings: binding.Table{Bind: tc.rows}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			r := &rig{t: t, s: s, logb: logb}
			if got := r.evalInt("(assoc-ref (model-info) 'num-ctx)"); got != tc.numCtx {
				t.Errorf("num-ctx %d, want %d", got, tc.numCtx)
			}
			if !strings.Contains(logb.String(), tc.note) {
				t.Errorf("the log does not say %q:\n%.2000s", tc.note, logb.String())
			}
			if strings.Contains(logb.String(), key) {
				t.Fatal("the key reached the log")
			}
		})
	}
}
