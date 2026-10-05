package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// An effort reaches a chat-completions server as reasoning_effort, and a
// request without one carries no such field: a server that does not know it
// may refuse it, so nothing is sent for a model nobody declared efforts for.
func TestOpenAISendsTheEffortChosen(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL, nil)
	for _, effort := range []string{"high", ""} {
		if _, err := o.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, Effort: effort}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := bodies[0]["reasoning_effort"]; got != "high" {
		t.Errorf("reasoning_effort with an effort chosen: %v", got)
	}
	if _, sent := bodies[1]["reasoning_effort"]; sent {
		t.Errorf("reasoning_effort sent with no effort chosen: %v", bodies[1])
	}
}

// Ollama's think takes a boolean for a model that thinks or not and a level
// for one that has levels; no effort sends nothing, which leaves the model to
// Ollama's own default.
func TestOllamaSendsTheEffortAsThink(t *testing.T) {
	for _, tc := range []struct {
		effort string
		want   string // the think field as JSON; "" when it is not sent
	}{
		{"", ""},
		{"on", `true`},
		{"off", `false`},
		{"low", `"low"`},
		{"high", `"high"`},
	} {
		srv := fake.New(fake.Turn{Content: "ok"})
		o := NewOllama(srv.URL)
		if _, err := o.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, Effort: tc.effort}, nil); err != nil {
			t.Fatal(err)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(srv.Requests[0].Raw, &body); err != nil {
			t.Fatal(err)
		}
		got, sent := body["think"]
		if tc.want == "" && sent {
			t.Errorf("effort %q: think sent as %s", tc.effort, got)
		}
		if tc.want != "" && string(got) != tc.want {
			t.Errorf("effort %q: think is %s, want %s", tc.effort, got, tc.want)
		}
		srv.Close()
	}
}

// A request without an effort marshals as it did before efforts existed, so
// the hashes of recorded requests still verify (ADR 2, nodwyn replay --window).
func TestRequestWithoutEffortMarshalsAsBefore(t *testing.T) {
	raw, err := json.Marshal(Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, has := fields["Effort"]; has {
		t.Fatalf("an empty effort is in the request's JSON: %s", raw)
	}
	withEffort, _ := json.Marshal(Request{Model: "m", Effort: "low"})
	if string(withEffort) == string(raw) {
		t.Fatalf("an effort does not change the request's hash: %s", withEffort)
	}
}

func TestValidEffort(t *testing.T) {
	for _, ok := range []string{"low", "medium", "high", "minimal", "xhigh", "on", "off", "max-2"} {
		if err := ValidEffort(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "High", "two words", "a-very-long-effort-word", "9high", "low,high", `lo"w`} {
		if err := ValidEffort(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A provider's refusal says which it was, and one that sending again cannot
// fix is not retried: a wrong key, a missing model or a refused field ends
// the turn at once, with what to do about it, where a server error is tried
// again.
func TestRefusalsAreStatusErrorsAndOnlyClientErrorsArePermanent(t *testing.T) {
	for _, tc := range []struct {
		status    int
		attempts  int
		hintWords string
	}{
		{http.StatusUnauthorized, 1, "key"},
		{http.StatusNotFound, 1, "no such model"},
		{http.StatusBadRequest, 1, "effort"},
		{http.StatusTooManyRequests, Retries + 1, "limiting"},
		{http.StatusInternalServerError, Retries + 1, ""},
	} {
		attempts := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			http.Error(w, `{"error":{"message":"nope"}}`, tc.status)
		}))
		old := RetryDelay
		RetryDelay = 0
		_, err := ChatRetrying(context.Background(), NewOpenAI(srv.URL, nil), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}},
			func(ctx context.Context) (context.Context, context.CancelFunc) { return context.WithCancel(ctx) }, nil)
		RetryDelay = old
		srv.Close()
		var se *StatusError
		if !errors.As(err, &se) || se.Status != tc.status || !strings.Contains(err.Error(), "nope") {
			t.Errorf("%d: %v", tc.status, err)
			continue
		}
		if attempts != tc.attempts {
			t.Errorf("%d: sent %d times, want %d", tc.status, attempts, tc.attempts)
		}
		if hint := Hint(err); (tc.hintWords == "") != (hint == "") || !strings.Contains(strings.ToLower(hint), tc.hintWords) {
			t.Errorf("%d: hint %q, want one saying %q", tc.status, hint, tc.hintWords)
		}
	}
}
