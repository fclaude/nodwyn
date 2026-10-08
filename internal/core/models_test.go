package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
	fakeoauth "github.com/fclaude/nodwyn/test/fakes/oauth"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// fakeOpenAI is a chat-completions server that answers with its replies in
// order and keeps the requests it got.
type fakeOpenAI struct {
	*httptest.Server
	mu       sync.Mutex
	replies  []string
	requests []seenRequest
}

type seenRequest struct {
	body, auth, session string
	header              http.Header
}

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
		f.requests = append(f.requests, seenRequest{string(body), r.Header.Get("Authorization"), r.Header.Get("X-Session-Id"), r.Header.Clone()})
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
	os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(init), 0o644)
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Models = models
	logb := &bytes.Buffer{}
	o.Config, o.Repo, o.Provider, o.LogWriter, o.AutoApprove = cfg, repo, llm.NewOllama(srv.URL), logb, true
	s, err := newInitSession(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: config.CanonicalPath(repo), logb: logb}
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
	t.Parallel()
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

// A session's requests to a chat-completions server name the session by a
// key derived from its id, the same in every request, so a server that
// routes by it can keep them on one prompt cache; cache_key = "off" sends
// none (ADR 9).
func TestModelRequestsCarryTheSessionsCacheKey(t *testing.T) {
	t.Parallel()
	init := `
(define (agent-turn request)
  (let loop ((reply (model-turn "impl")))
    (unless (null? (assoc-ref reply 'tool-calls '()))
      (run-tools (assoc-ref reply 'tool-calls))
      (loop (model-turn "impl")))))`
	for _, setting := range []string{"", config.CacheKeyHeader, config.CacheKeyOff} {
		impl := newFakeOpenAI(t, oaiCall("c1", "fs-list", `{"path":"."}`), oaiText("listed"))
		r := modelRig(t, init, map[string]config.Model{"impl": {Provider: "openai", URL: impl.URL + "/v1", Model: "m", CacheKey: setting}}, Options{})
		if err := r.turn("list files"); err != "" {
			t.Fatal(err)
		}
		key := cacheKey(r.s.ID())
		if setting == config.CacheKeyOff {
			key = ""
		}
		for i, req := range impl.seen() {
			inBody := strings.Contains(req.body, `"prompt_cache_key":"`+key+`"`)
			if req.session != key || inBody != (setting == "") || strings.Contains(req.body, r.s.ID()) {
				t.Errorf("cache_key %q, request %d: X-Session-Id %q, body %s", setting, i, req.session, req.body)
			}
		}
	}
	if len(cacheKey("a")) != 32 || cacheKey("a") == cacheKey("b") {
		t.Fatalf("keys %q %q", cacheKey("a"), cacheKey("b"))
	}
}

// A ChatGPT plan's requests name the session by the headers Codex sends as
// well, since by prompt_cache_key alone the plan's server read almost none of
// a session from its cache. An API key's requests, which it does cache, are
// as they were, and cache_key = "off" sends neither (ADR 9, amended
// 2026-10-07).
func TestPlanRequestsCarryCodexSessionHeaders(t *testing.T) {
	t.Parallel()
	issuer := fakeoauth.New("nodwyn-test")
	t.Cleanup(issuer.Close)
	plan := []string{"openid", "resource.invoke", "chatgpt.tokens.use.direct"}
	for _, tc := range []struct {
		credential, setting string
		named               bool
	}{
		{"plan", "", true},
		{"plan", config.CacheKeyHeader, true},
		{"plan", config.CacheKeyOff, false},
		{"key", "", false},
	} {
		kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
		if err != nil {
			t.Fatal(err)
		}
		g := keychain.Grant{Profile: keychain.Custom(issuer.AuthURL(), issuer.TokenURL(), ""), ClientID: issuer.ClientID, Scopes: plan}
		if _, err := kc.AddOAuth2(context.Background(), "plan", g, issuer.Browser); err != nil {
			t.Fatal(err)
		}
		if _, err := kc.Add("key", []byte("sk-test-key")); err != nil {
			t.Fatal(err)
		}
		table := binding.Table{Bind: []binding.Binding{
			{Class: "plan", Dest: "127.0.0.1", Positions: []string{"header:Authorization:Bearer"}, Methods: []string{"POST"}, Scopes: plan},
			{Class: "key", Dest: "127.0.0.1", Positions: []string{"header:Authorization:Bearer"}, Methods: []string{"POST"}},
		}}
		impl := newFakeOpenAI(t, oaiText("hello"))
		r := modelRig(t, `(define (agent-turn request) (model-turn "impl"))`,
			map[string]config.Model{"impl": {Provider: "openai", URL: impl.URL + "/v1", Model: "m", Credential: tc.credential, CacheKey: tc.setting}},
			Options{Keychain: kc, Bindings: table})
		if e := r.turn("hi"); e != "" {
			t.Fatal(e)
		}
		want := ""
		if tc.named {
			want = cacheKey(r.s.ID())
		}
		seen := impl.seen()
		if len(seen) != 1 || seen[0].auth == "" {
			t.Fatalf("%s, cache_key %q: requests %+v", tc.credential, tc.setting, seen)
		}
		for _, name := range []string{"session-id", "thread-id", "x-client-request-id"} {
			if got := seen[0].header.Get(name); got != want {
				t.Errorf("%s, cache_key %q: %s %q, want %q", tc.credential, tc.setting, name, got, want)
			}
		}
	}
}

func TestModelTurnUnknownName(t *testing.T) {
	t.Parallel()
	r := modelRig(t, `(define (agent-turn request) (model-turn "gpt-9"))`,
		map[string]config.Model{"impl": {Provider: "ollama", URL: "http://127.0.0.1:1", Model: "m"}}, Options{})
	if err := r.turn("hi"); !strings.Contains(err, `no model "gpt-9" is configured; configured models: control, impl`) {
		t.Fatalf("turn error %q", err)
	}
}

// A keyed model gets its key only where the key is bound, like any other
// credential; the check is logged and the key never is.
func TestKeyedModelNeedsItsBinding(t *testing.T) {
	t.Parallel()
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

// A keyed model whose bound host redirects is not followed to the host it
// names, which no binding covers: the turn fails saying where the server
// pointed, the request is not sent again, and neither the key nor the
// window reaches the other host.
func TestKeyedModelRedirectIsNotFollowed(t *testing.T) {
	t.Parallel()
	const secret, window = "sk-model-redirect-canary-3141", "window-redirect-canary-3141"
	var mu sync.Mutex
	var elsewhere []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dump, _ := httputil.DumpRequest(r, true)
		mu.Lock()
		elsewhere = append(elsewhere, string(dump))
		mu.Unlock()
	}))
	t.Cleanup(other.Close)
	otherHost := "localhost:" + other.URL[strings.LastIndex(other.URL, ":")+1:]
	var sent atomic.Int32
	bound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		http.Redirect(w, r, "http://"+otherHost+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(bound.Close)
	kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
	if err != nil {
		t.Fatal(err)
	}
	kc.Add("impl-key", []byte(secret))
	r := modelRig(t, `(define (agent-turn request) (model-turn "impl"))`,
		map[string]config.Model{"impl": {Provider: "openai", URL: bound.URL + "/v1", Model: "m", Credential: "impl-key"}},
		Options{Keychain: kc, Bindings: binding.Table{Bind: []binding.Binding{{Class: "impl-key", Dest: "127.0.0.1", Positions: []string{"header:Authorization:Bearer"}, Methods: []string{"POST"}}}}})
	e := r.turn(window)
	if want := "redirected to " + otherHost + "; nodwyn does not follow redirects for model requests"; !strings.Contains(e, want) {
		t.Fatalf("turn error %q, want it to say %q", e, want)
	}
	if n := sent.Load(); n != 1 {
		t.Fatalf("the bound host got %d requests; a redirect is not asked again", n)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, req := range elsewhere {
		if strings.Contains(req, secret) || strings.Contains(req, window) {
			t.Errorf("the key or the window reached %s:\n%s", otherHost, req)
		}
	}
	if len(elsewhere) != 0 {
		t.Fatalf("the redirect was followed: %s got %d requests", otherHost, len(elsewhere))
	}
	if strings.Contains(r.logb.String(), secret) {
		t.Fatal("the model's key reached the log")
	}
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// Without a [control] model a session still opens, to replay or evaluate;
// its first model request says what to add, and is not retried.
func TestMissingControlFailsAtTheFirstModelRequest(t *testing.T) {
	t.Parallel()
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: config.Default(), Repo: t.TempDir(), NoConfirm: true, LogWriter: logb})
	if err != nil {
		t.Fatalf("a session without a control model did not open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	r := &rig{t: t, s: s, logb: logb}
	if e := r.turn("hi"); !strings.Contains(e, "no control model is configured") || !strings.Contains(e, "[control]") {
		t.Fatalf("turn error %q", e)
	}
	if strings.Contains(logb.String(), "request failed, retrying") {
		t.Fatal("a missing control model was retried")
	}
}

// The control model is built like any configured model, so it may be a
// keyed chat-completions server whose key is bound.
func TestKeyedControlModel(t *testing.T) {
	t.Parallel()
	const secret = "sk-control-canary-7788"
	ctl := newFakeOpenAI(t, oaiText("hello from a keyed control model"))
	kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
	if err != nil {
		t.Fatal(err)
	}
	kc.Add("control-key", []byte(secret))
	cfg := config.Default()
	cfg.Control = config.Model{Provider: "openai", URL: ctl.URL + "/v1", Model: "coder", NumCtx: 128000, Credential: "control-key"}
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), NoConfirm: true, LogWriter: logb, Keychain: kc,
		Bindings: binding.Table{Bind: []binding.Binding{{Class: "control-key", Dest: "127.0.0.1", Positions: []string{"header:Authorization:Bearer"}, Methods: []string{"POST"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := &rig{t: t, s: s, logb: logb}
	if e := r.turn("hi"); e != "" {
		t.Fatal(e)
	}
	seen := ctl.seen()
	if len(seen) != 1 || seen[0].auth != "Bearer "+secret || !strings.Contains(seen[0].body, `"model":"coder"`) {
		t.Fatalf("control requests: %+v", seen)
	}
	if !strings.Contains(logb.String(), `"provider":"openai"`) || strings.Contains(logb.String(), secret) {
		t.Fatal("session.start should name the provider, and the log must not hold the key")
	}
	if n := replayAll(t, logb); n != 1 {
		t.Fatalf("replayed %d requests", n)
	}
}

// An ollama model without num_ctx is asked for the window's size, not left to
// Ollama's own default context, which would cut the prompt silently.
func TestOllamaIsAskedForTheWindowsContext(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "", 10, fake.Turn{Content: "done"})
	if r.turn("hello") != "" {
		t.Fatal("turn failed")
	}
	want := `"num_ctx":` + strconv.Itoa(config.Default().WindowNumCtx(0))
	if !strings.Contains(string(r.srv.Requests[0].Raw), want) {
		t.Fatalf("request does not ask for %s:\n%s", want, r.srv.Requests[0].Raw)
	}
}

// A chat-completions control model without num_ctx is sized by what the
// server's model list says of its context; a model the list does not give
// a length for keeps the default window, and the log says why.
func TestWindowSizedFromTheModelList(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"data":[{"id":"long","context_length":1048576},{"id":"quiet"}]}`))
		case "/v1/chat/completions":
			w.Write([]byte(`{"choices":[{"message":` + oaiText("done") + `,"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	for _, tc := range []struct {
		model            string
		high, ceiling, n int
		note             string
	}{
		{"long", 774144, 1032192, 1048576, "sized for long's context of 1048576 tokens"},
		{"quiet", 61440, 61440, 0, "set it for the model in Settings › Models, or as num_ctx in its entry"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			cfg := config.Default()
			cfg.Control = config.Model{Provider: "openai", URL: srv.URL + "/v1", Model: tc.model}
			logb := &bytes.Buffer{}
			s, err := New(Options{Config: cfg, Repo: t.TempDir(), NoConfirm: true, LogWriter: logb})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			r := &rig{t: t, s: s, logb: logb}
			if got := r.evalInt("(assoc-ref (window-limits) 'high-water)"); got != tc.high {
				t.Errorf("high water %d, want %d", got, tc.high)
			}
			if got := r.evalInt("(assoc-ref (window-limits) 'ceiling)"); got != tc.ceiling {
				t.Errorf("ceiling %d, want %d", got, tc.ceiling)
			}
			if got := r.evalInt("(assoc-ref (model-info) 'num-ctx)"); got != tc.n {
				t.Errorf("num-ctx %d, want %d", got, tc.n)
			}
			if !strings.Contains(logb.String(), tc.note) {
				t.Errorf("the log does not say %q", tc.note)
			}
			if e := r.turn("hi"); e != "" {
				t.Fatal(e)
			}
			if n := replayAll(t, logb); n != 1 {
				t.Fatalf("replayed %d requests", n)
			}
		})
	}
}

// A Responses model with keep_reasoning keeps each reply's encrypted
// reasoning in the log and gives it back before the call it came with, and a
// later turn still sends it where it was, so a turn's requests stay the start
// of the next turn's. Without the setting none is kept or asked for. Every
// request still replays from the log (ADR 9, amended 2026-10-07).
func TestKeptReasoningGoesBackWithItsReply(t *testing.T) {
	t.Parallel()
	init := `
(define (agent-turn request)
  (let loop ((reply (model-turn "impl")))
    (unless (null? (assoc-ref reply 'tool-calls '()))
      (run-tools (assoc-ref reply 'tool-calls))
      (loop (model-turn "impl")))))`
	replies := []string{
		`{"status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"list first"}],"encrypted_content":"enc-1"},` +
			`{"type":"function_call","call_id":"c1","name":"fs-list","arguments":"{\"path\":\".\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"listed"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"again"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`,
	}
	for _, keep := range []bool{true, false} {
		var mu sync.Mutex
		var bodies []map[string]json.RawMessage
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]json.RawMessage
			json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) == len(replies) {
				http.Error(w, `{"error":{"message":"script exhausted"}}`, http.StatusInternalServerError)
				return
			}
			bodies = append(bodies, body)
			w.Write([]byte(replies[len(bodies)-1]))
		}))
		t.Cleanup(srv.Close)
		m := config.Model{Provider: "openai", Dialect: llm.ChatResponses, URL: srv.URL + "/v1", Model: "m", KeepReasoning: keep}
		r := modelRig(t, init, map[string]config.Model{"impl": m}, Options{})
		if e := r.turn("list files"); e != "" {
			t.Fatal(e)
		}
		if e := r.turn("again"); e != "" {
			t.Fatal(e)
		}
		mu.Lock()
		got := append([]map[string]json.RawMessage(nil), bodies...)
		mu.Unlock()
		if len(got) != 3 {
			t.Fatalf("keep %v: %d requests", keep, len(got))
		}
		item := `{"type":"reasoning","summary":[{"type":"summary_text","text":"list first"}],"encrypted_content":"enc-1"},{"type":"function_call","call_id":"c1"`
		for i, body := range got {
			asked := string(body["include"]) == `["reasoning.encrypted_content"]` && string(body["reasoning"]) == `{"context":"all_turns"}`
			sent := strings.Contains(string(body["input"]), item)
			if asked != keep || sent != (keep && i > 0) {
				t.Errorf("keep %v, request %d: include %s, reasoning %s, input %s", keep, i, body["include"], body["reasoning"], body["input"])
			}
		}
		second, third := string(got[1]["input"]), string(got[2]["input"])
		if !strings.HasPrefix(third, strings.TrimSuffix(second, "]")) {
			t.Errorf("keep %v: the second turn changed what the first sent:\n%s\n%s", keep, second, third)
		}
		if logged := strings.Contains(r.logb.String(), `"data":"enc-1"`); logged != keep {
			t.Errorf("keep %v: the reasoning was logged: %v", keep, logged)
		}
		if n := replayAll(t, r.logb); n != 3 {
			t.Fatalf("keep %v: replayed %d requests", keep, n)
		}
	}
}
