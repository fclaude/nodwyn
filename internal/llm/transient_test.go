package llm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// relayed404 is the answer OpenRouter gave one request of an unattended
// lean session on 2026-10-17 (Muse through a local proxy): the provider it
// routed the request to said it had no such model, though thousands of
// requests to the same model succeeded before and after it. The raw message
// is cut in the session's note; the rest is as it came.
const relayed404 = `{"error":{"message":"Provider returned error","code":404,"metadata":{"raw":"{\"error\":{\"code\":\"model_not_found\",\"message\":\"The requested model was not found.\"}}","provider_name":"Meta"}}}`

const okReply = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

// scripted is an OpenAI-compatible server that gives its failures in order,
// one a request, and then answers every request with okReply. A failure is a
// status and body, or a connection it drops before answering.
type scripted struct {
	*httptest.Server
	mu       sync.Mutex
	failures []failure
	requests int
}

type failure struct {
	status int
	body   string
	// drop closes the connection with no answer: reset sends a TCP reset,
	// else it is closed as an EOF.
	drop, reset bool
}

func newScripted(t *testing.T, failures ...failure) *scripted {
	s := &scripted{failures: failures}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests++
		var f *failure
		if len(s.failures) > 0 {
			f = &s.failures[0]
			s.failures = s.failures[1:]
		}
		s.mu.Unlock()
		switch {
		case f == nil:
			w.Write([]byte(okReply))
		case f.drop:
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			if tcp, ok := conn.(*net.TCPConn); ok && f.reset {
				tcp.SetLinger(0)
			}
			conn.Close()
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.status)
			w.Write([]byte(f.body))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *scripted) seen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func quickRetries(t *testing.T, d time.Duration) {
	t.Helper()
	old := RetryDelay
	RetryDelay = d
	t.Cleanup(func() { RetryDelay = old })
}

var hello = Request{Model: "meta/muse-spark-1.3", Messages: []Message{{Role: RoleUser, Content: "hi"}}}

func eachAttempt(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 5*time.Second)
}

// A relay's report that the provider it routed the request to failed is sent
// again, whatever status it carries: the 404 inside it is the provider's
// failure, not this request's, and the next try is answered (ADR 41, amended
// 2026-10-17). Each retry is told to onRetry, which the loop records.
func TestARelayedProviderFailureIsSentAgain(t *testing.T) {
	quickRetries(t, time.Millisecond)
	srv := newScripted(t, failure{status: http.StatusNotFound, body: relayed404})
	var retried []error
	resp, err := ChatRetrying(context.Background(), NewOpenAI(srv.URL+"/v1", nil), hello, eachAttempt,
		func(_ int, err error) { retried = append(retried, err) })
	if err != nil || resp.Message.Content != "ok" {
		t.Fatalf("the reply did not arrive: %v %+v", err, resp)
	}
	if srv.seen() != 2 || len(retried) != 1 || !strings.Contains(retried[0].Error(), "404 Not Found") || !strings.Contains(retried[0].Error(), "Provider returned error") {
		t.Fatalf("sent %d times, retries told %v", srv.seen(), retried)
	}
}

// A relayed failure that goes on is sent Retries more times and no more, and
// the turn's error says the provider failed, not that the model is unknown.
func TestARelayedProviderFailureIsRetriedUpToTheBound(t *testing.T) {
	quickRetries(t, time.Millisecond)
	srv := newScripted(t, failure{status: 404, body: relayed404}, failure{status: 404, body: relayed404},
		failure{status: 404, body: relayed404}, failure{status: 404, body: relayed404})
	retries := 0
	_, err := ChatRetrying(context.Background(), NewOpenAI(srv.URL+"/v1", nil), hello, eachAttempt, func(int, error) { retries++ })
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 404 || srv.seen() != Retries+1 || retries != Retries {
		t.Fatalf("sent %d times with %d retries, want %d and %d: %v", srv.seen(), retries, Retries+1, Retries, err)
	}
	if hint := Hint(err); !strings.Contains(hint, "provider") || strings.Contains(hint, "no such model") {
		t.Fatalf("hint %q", hint)
	}
}

// A retry's wait ends when the caller's context does: the run's deadline or
// a cancel stops it at once, and nothing is sent after it.
func TestARelayedProviderFailureWaitsNoLongerThanTheCaller(t *testing.T) {
	quickRetries(t, time.Hour)
	for _, stop := range []string{"deadline", "cancel"} {
		srv := newScripted(t, failure{status: 404, body: relayed404})
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		retries := 0
		onRetry := func(int, error) {
			retries++
			if stop == "cancel" {
				cancel()
			}
		}
		start := time.Now()
		_, err := ChatRetrying(ctx, NewOpenAI(srv.URL+"/v1", nil), hello, eachAttempt, onRetry)
		took := time.Since(start)
		cancel()
		var se *StatusError
		if !errors.As(err, &se) || se.Status != 404 {
			t.Errorf("%s: %v", stop, err)
		}
		if retries != 1 || srv.seen() != 1 || took > 5*time.Second {
			t.Errorf("%s: %d retries told, sent %d times, returned after %v; want one retry waited for, cut by the caller", stop, retries, srv.seen(), took)
		}
	}
}

// What sending again cannot fix is not sent again: a key the server refuses,
// a model a provider itself says it does not have (OpenAI's words, Ollama's),
// a relay's own refusal, a request over the model's context however it is
// answered, and a relayed provider failure that says the request or the key
// was refused.
func TestRefusalsThatWouldFailAgainAreNotRetried(t *testing.T) {
	quickRetries(t, time.Millisecond)
	overContext := `{"error":{"message":"This model's maximum context length is 131072 tokens. However, your messages resulted in 140210 tokens.","type":"invalid_request_error","code":"context_length_exceeded"}}`
	for _, f := range []failure{
		{status: 401, body: `{"error":{"message":"Incorrect API key provided.","type":"invalid_request_error","code":"invalid_api_key"}}`},
		{status: 404, body: "{\"error\":{\"message\":\"The model `muse` does not exist or you do not have access to it.\",\"type\":\"invalid_request_error\",\"code\":\"model_not_found\"}}"},
		{status: 404, body: `{"error":"model 'qwen3:32b' not found, try pulling it first"}`},
		{status: 404, body: `{"error":{"message":"No endpoints found that support tool use.","code":404}}`},
		{status: 400, body: overContext},
		{status: 500, body: overContext},
		{status: 404, body: `{"error":{"message":"Provider returned error","code":404,"metadata":{"raw":"{\"error\":{\"message\":\"prompt is too long: 210000 tokens > 200000 maximum\"}}"}}}`},
		{status: 400, body: `{"error":{"message":"Provider returned error","code":400,"metadata":{"raw":"{\"error\":{\"message\":\"Invalid schema for function\"}}"}}}`},
		{status: 401, body: `{"error":{"message":"Provider returned error","code":401,"metadata":{"raw":"invalid api key"}}}`},
		{status: 403, body: `{"error":{"message":"Provider returned error","code":403,"metadata":{"raw":"forbidden"}}}`},
	} {
		srv := newScripted(t, f, f, f)
		_, err := ChatRetrying(context.Background(), NewOpenAI(srv.URL+"/v1", nil), hello, eachAttempt,
			func(int, error) { t.Errorf("%d %s: retried", f.status, f.body) })
		var se *StatusError
		if !errors.As(err, &se) || se.Status != f.status || srv.seen() != 1 {
			t.Errorf("%d %s: sent %d times: %v", f.status, f.body, srv.seen(), err)
		}
	}
}

// A provider that is busy, failing or overloaded, a relay that says the
// provider failed, and a connection refused, reset or closed before any
// answer are sent again, and the next try is answered.
func TestTransientFailuresAreSentAgain(t *testing.T) {
	quickRetries(t, time.Millisecond)
	for _, f := range []failure{
		{status: 429, body: `{"error":{"message":"Rate limit exceeded: tokens per minute","code":429}}`},
		{status: 500, body: `{"error":{"message":"Internal server error"}}`},
		{status: 502, body: `{"error":{"message":"Provider returned error","code":502,"metadata":{"raw":"upstream connect error"}}}`},
		{status: 503, body: `{"error":{"message":"No instances available","code":503}}`},
		{status: 504, body: `<html>Gateway Timeout</html>`},
		{status: 529, body: `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
		{drop: true, reset: true},
		{drop: true},
	} {
		srv := newScripted(t, f)
		retries := 0
		resp, err := ChatRetrying(context.Background(), NewOpenAI(srv.URL+"/v1", nil), hello, eachAttempt, func(int, error) { retries++ })
		if err != nil || resp.Message.Content != "ok" || srv.seen() != 2 || retries != 1 {
			t.Errorf("%+v: sent %d times, %d retries: %v", f, srv.seen(), retries, err)
		}
	}
	// A connection refused: the first dial goes to a port nobody listens on.
	srv := newScripted(t)
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refusedAt := closed.Addr().String()
	closed.Close()
	var dials int
	o := NewOpenAI(srv.URL+"/v1", nil)
	o.Client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials++
		if dials == 1 {
			addr = refusedAt
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	retries := 0
	resp, err := ChatRetrying(context.Background(), o, hello, eachAttempt, func(_ int, err error) {
		retries++
		if !strings.Contains(err.Error(), "refused") {
			t.Errorf("the first failure was %v, not a refused connection", err)
		}
	})
	if err != nil || resp.Message.Content != "ok" || retries != 1 || srv.seen() != 1 {
		t.Fatalf("a refused connection: %d retries, sent %d times: %v", retries, srv.seen(), err)
	}
}
