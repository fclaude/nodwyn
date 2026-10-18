package core

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

// relayed404 is what OpenRouter answered one model request of an unattended
// lean session on 2026-10-17 (Muse, through a local proxy), the only request
// of the evening that failed: the provider it routed the request to said it
// had no such model. The session ended at once ("turn ended: error: openai:
// 404 Not Found"). The raw message is cut in the session's note.
const relayed404 = `{"error":{"message":"Provider returned error","code":404,"metadata":{"raw":"{\"error\":{\"code\":\"model_not_found\",\"message\":\"The requested model was not found.\"}}","provider_name":"Meta"}}}`

// failingOnce is an OpenAI-compatible server that answers its first chat
// completion with status and body (every one when always), and every later
// one with a reply. Its model list gives the model's context length.
type failingOnce struct {
	*httptest.Server
	mu       sync.Mutex
	answered []int
}

func newFailingOnce(t *testing.T, status int, body string, always bool) *failingOnce {
	f := &failingOnce{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[{"id":"meta/muse-spark-1.3","context_length":262144}]}`))
			return
		}
		f.mu.Lock()
		fail := always || len(f.answered) == 0
		if fail {
			f.answered = append(f.answered, status)
		} else {
			f.answered = append(f.answered, http.StatusOK)
		}
		f.mu.Unlock()
		if fail {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			w.Write([]byte(body))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"The repository says hello."},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *failingOnce) statuses() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.answered...)
}

// providerRig is a session nobody approves whose control model is on srv, an
// OpenAI-compatible server, as a model through OpenRouter is.
func providerRig(t *testing.T, srv *httptest.Server) *rig {
	t.Helper()
	cfg := config.Default()
	cfg.Control = config.Model{Provider: "openai", URL: srv.URL + "/v1", Model: "meta/muse-spark-1.3"}
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), Version: "test", Provider: llm.NewOpenAI(srv.URL+"/v1", nil), AutoApprove: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, logb: logb}
}

// A relay's report that the provider it routed a request to failed is sent
// again, in either profile, and the reply arrives; the log says it was
// retried (ADR 41, amended 2026-10-17). The same status from a provider that
// says itself it has no such model still ends the turn at once, with what to
// change.
func TestARelayedProviderFailureDoesNotEndTheSession(t *testing.T) {
	// llm.RetryDelay is the whole process's, and the profile the
	// environment's, so this test is not parallel.
	defer func(d time.Duration) { llm.RetryDelay = d }(llm.RetryDelay)
	llm.RetryDelay = time.Millisecond
	for _, profile := range []string{config.ProfileLean, config.ProfileFull} {
		useProfile(t, profile)
		srv := newFailingOnce(t, http.StatusNotFound, relayed404, false)
		r := providerRig(t, srv.Server)
		if r.s.profile != profile {
			t.Fatalf("profile %q, want %q", r.s.profile, profile)
		}
		if e := r.turn("say hello"); e != "" {
			t.Fatalf("%s: the turn ended with %s", profile, e)
		}
		if got := srv.statuses(); len(got) < 2 || got[0] != 404 || got[1] != 200 {
			t.Fatalf("%s: answered %v", profile, got)
		}
		retried := false
		for _, n := range r.notes() {
			retried = retried || strings.HasPrefix(n, "model request failed, retrying (1/2): openai: 404 Not Found: ") && strings.Contains(n, "Provider returned error")
		}
		if !retried {
			t.Errorf("%s: no note says the request was sent again: %q", profile, r.notes())
		}
		var replied bool
		for _, ev := range r.logged() {
			var resp log.LLMResp
			if ev.Kind == log.KindLLMResp && ev.Decode(&resp) == nil && resp.Content == "The repository says hello." {
				replied = true
			}
		}
		if !replied {
			t.Errorf("%s: the reply is not in the log", profile)
		}
	}

	useProfile(t, config.ProfileLean)
	direct := newFailingOnce(t, http.StatusNotFound, "{\"error\":{\"message\":\"The model `meta/muse-spark-1.3` does not exist or you do not have access to it.\",\"type\":\"invalid_request_error\",\"code\":\"model_not_found\"}}", true)
	r := providerRig(t, direct.Server)
	if e := r.turn("say hello"); !strings.Contains(e, "404 Not Found") || !strings.Contains(e, "no such model") {
		t.Fatalf("a provider's own 404 ended the turn with %q", e)
	}
	if got := direct.statuses(); len(got) != 1 {
		t.Fatalf("a provider's own 404 was sent %d times", len(got))
	}
}
