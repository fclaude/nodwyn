package core

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

// A task, a model probe and an MCP inspection send a key by one authorizer
// (P2-008): the same refusals, the same permanence, a binding table that
// decides, and the log written only when a recorder is given.
func TestKeyedAuthorizesAKeyedRequest(t *testing.T) {
	store, err := keychain.Create(filepath.Join(t.TempDir(), "keychain"), keychain.FastKDF, []byte("pass"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("svc", []byte("canary-key")); err != nil {
		t.Fatal(err)
	}
	row, err := binding.Exact("svc", "api.example.com", "POST", "header:Authorization:Bearer")
	if err != nil {
		t.Fatal(err)
	}
	local, err := binding.Exact("svc", "127.0.0.2", "POST", "header:Authorization:Bearer")
	if err != nil {
		t.Fatal(err)
	}
	table := binding.Table{Bind: []binding.Binding{row, local}}
	var checks []log.EgressCheck
	k := Keyed{Keychain: store, Locked: "locked", Table: table, Egress: func(e log.EgressCheck) error { checks = append(checks, e); return nil }}
	send := func(k Keyed, url, position string) (*http.Request, error) {
		req, _ := http.NewRequestWithContext(context.Background(), "POST", url, nil)
		return req, k.Authorize("svc", position)(req)
	}

	req, err := send(k, "https://api.example.com/v1", "header:Authorization:Bearer")
	if err != nil || req.Header.Get("Authorization") != "Bearer canary-key" || len(checks) != 1 || !checks[0].Bound {
		t.Fatalf("a bound request: %v %v %+v", err, req.Header, checks)
	}
	// Plain http to a loopback address that is not 127.0.0.1 is this machine.
	if _, err := send(k, "http://127.0.0.2:8000/v1", "header:Authorization:Bearer"); err != nil {
		t.Fatalf("plain http to 127.0.0.2: %v", err)
	}
	// A query position injects into the URL, as an MCP server may ask.
	qrow, _ := binding.Exact("svc", "api.example.com", "POST", "query:key")
	q := k
	q.Table = binding.Table{Bind: []binding.Binding{qrow}}
	if req, err := send(q, "https://api.example.com/v1", "query:key"); err != nil || req.URL.Query().Get("key") != "canary-key" {
		t.Fatalf("a query position: %v %v", err, req.URL)
	}

	// A refusal is permanent: sent through the retrying path, the request is
	// authorized once, not three times.
	for name, tc := range map[string]struct {
		k        Keyed
		url, pos string
	}{
		"unbound host":      {k, "https://other.example.com/v1", "header:Authorization:Bearer"},
		"unbound position":  {k, "https://api.example.com/v1", "header:X-Key:"},
		"plain http remote": {k, "http://api.example.com/v1", "header:Authorization:Bearer"},
		"locked":            {Keyed{Locked: "locked", Table: table}, "https://api.example.com/v1", "header:Authorization:Bearer"},
	} {
		asked := &authorizing{authorize: tc.k.Authorize("svc", tc.pos), url: tc.url}
		_, err := llm.ChatRetrying(context.Background(), asked, llm.Request{}, func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		}, nil)
		if err == nil || asked.calls != 1 {
			t.Errorf("%s: %v after %d attempts, want a refusal after 1", name, err, asked.calls)
		}
	}
	// A probe passes no recorder and nothing is logged for it.
	before := len(checks)
	probe := Keyed{Keychain: store, Table: table}
	if _, err := send(probe, "https://api.example.com/v1", "header:Authorization:Bearer"); err != nil || len(checks) != before {
		t.Fatalf("a probe: %v, %d new checks", err, len(checks)-before)
	}
}

// authorizing is a provider whose request needs a key: it only authorizes.
type authorizing struct {
	authorize func(*http.Request) error
	url       string
	calls     int
}

func (a *authorizing) Name() string { return "authorizing" }
func (a *authorizing) Chat(ctx context.Context, _ llm.Request, _ func(llm.Delta)) (*llm.Response, error) {
	a.calls++
	req, _ := http.NewRequestWithContext(ctx, "POST", a.url, nil)
	if err := a.authorize(req); err != nil {
		return nil, err
	}
	return &llm.Response{}, nil
}
func (a *authorizing) Ping(context.Context) (string, error) { return "", nil }
