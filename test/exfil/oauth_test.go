package exfil

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/keychain"
	fakeoauth "github.com/fclaude/nodwyn/test/fakes/oauth"
)

// OAuth2 credentials through a real session (ADR 24): refreshed only for a
// use the binding table allows, every refresh logged and no token anywhere
// in the log, a dead refresh token denying instead of asking, and a token
// granted more scopes than its binding names refused.

type oauthRig struct {
	sess     *core.Session
	log      *strings.Builder
	provider *fakeoauth.Server
	api      *httptest.Server
	mu       sync.Mutex
	got      []string // Authorization headers the API received
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// newOAuthRig stores an oauth2 credential of class docs, granted
// files.read and files.write, whose access token is already stale, and
// opens a session. row is the binding for the API host, or nil for none.
func newOAuthRig(t *testing.T, row *binding.Binding) *oauthRig {
	t.Helper()
	r := &oauthRig{log: &strings.Builder{}}
	r.provider = fakeoauth.New("nodwyn-test")
	t.Cleanup(r.provider.Close)
	r.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.got = append(r.got, req.Header.Get("Authorization"))
		r.mu.Unlock()
		w.Write([]byte("ok"))
	}))
	t.Cleanup(r.api.Close)

	kc, err := keychain.Create(t.TempDir()+"/kc", keychain.FastKDF, []byte("test-pass"))
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Now()}
	kc.SetClock(clock.Now)
	g := keychain.Grant{Profile: keychain.Custom(r.provider.AuthURL(), r.provider.TokenURL(), r.provider.RevokeURL()),
		ClientID: r.provider.ClientID, Scopes: []string{"files.read", "files.write"}}
	if _, err := kc.AddOAuth2(context.Background(), "docs", g, r.provider.Browser); err != nil {
		t.Fatal(err)
	}
	clock.mu.Lock()
	clock.now = clock.now.Add(2 * time.Hour)
	clock.mu.Unlock()

	var tbl binding.Table
	if row != nil {
		tbl.Bind = []binding.Binding{*row}
	}
	r.sess, err = core.New(core.Options{Config: config.Default(), Repo: t.TempDir(), NoConfirm: true, LogWriter: r.log, Keychain: kc, Bindings: tbl})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range r.sess.Events() {
		}
	}()
	t.Cleanup(func() { r.sess.Close() })
	return r
}

func docsRow(scopes ...string) *binding.Binding {
	return &binding.Binding{Class: "docs", Dest: "127.0.0.1", Positions: []string{"header:Authorization:Bearer"}, Methods: []string{"GET"}, Scopes: scopes}
}

func (r *oauthRig) call(t *testing.T) error {
	t.Helper()
	_, err := r.sess.Eval(context.Background(), `(http-request (credential "docs") "`+r.api.URL+`/v1/files" "GET" "header:Authorization:Bearer" '() "")`)
	return err
}

func (r *oauthRig) received() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

// noTokens fails if any token the provider issued reached the session log.
func (r *oauthRig) noTokens(t *testing.T) {
	t.Helper()
	if fakeoauth.HasToken(r.log.String()) {
		t.Fatalf("a token reached the session log:\n%s", r.log.String())
	}
}

func TestOAuth2RefreshedBeforeUseAndLogged(t *testing.T) {
	r := newOAuthRig(t, docsRow())
	if err := r.call(t); err != nil {
		t.Fatal(err)
	}
	if r.provider.Refreshes() != 1 {
		t.Fatalf("want one refresh, got %d", r.provider.Refreshes())
	}
	got := r.received()
	if len(got) != 1 || !strings.HasPrefix(got[0], "Bearer fake-access-token-") {
		t.Fatalf("the API got %v", got)
	}
	log := r.log.String()
	if !strings.Contains(log, `"kind":"cred.refresh"`) || !strings.Contains(log, `"endpoint":"127.0.0.1"`) || !strings.Contains(log, `"ok":true`) {
		t.Fatalf("no cred.refresh record:\n%s", log)
	}
	if !strings.Contains(log, `"scopes":["files.read","files.write"]`) {
		t.Fatalf("the egress check does not name the scopes:\n%s", log)
	}
	r.noTokens(t)
}

// A use the binding table refuses never reaches the token endpoint: the
// refresh is part of an allowed use, never a request of its own.
func TestOAuth2UnboundUseIsNotRefreshed(t *testing.T) {
	r := newOAuthRig(t, nil)
	err := r.call(t)
	var d *scheme.Denied
	if !errors.As(err, &d) {
		t.Fatalf("want a denial, got %v", err)
	}
	if r.provider.Refreshes() != 0 || len(r.received()) != 0 {
		t.Fatalf("refreshes %d, API requests %v", r.provider.Refreshes(), r.received())
	}
	r.noTokens(t)
}

// A token granted more scopes than the binding names is refused before
// anything is sent, the refresh included.
func TestOAuth2ScopesBeyondTheBindingDenied(t *testing.T) {
	r := newOAuthRig(t, docsRow("files.read"))
	err := r.call(t)
	var d *scheme.Denied
	if !errors.As(err, &d) || !strings.Contains(d.Reason, "scope files.write of the docs credential") {
		t.Fatalf("want a scope denial, got %v", err)
	}
	if r.provider.Refreshes() != 0 || len(r.received()) != 0 {
		t.Fatal("something was sent")
	}
	r = newOAuthRig(t, docsRow("files.read", "files.write"))
	if err := r.call(t); err != nil {
		t.Fatalf("a row naming every granted scope: %v", err)
	}
}

// A refresh token the provider no longer honours denies the use, names the
// command that fixes it, and asks nothing: no confirm, no browser.
func TestOAuth2NeedsReauthDeniesWithoutAsking(t *testing.T) {
	r := newOAuthRig(t, docsRow())
	r.provider.RevokeAll()
	for i := 0; i < 2; i++ {
		err := r.call(t)
		if err == nil || !strings.Contains(err.Error(), "nodwyn credential login") {
			t.Fatalf("call %d: want a re-authorize denial, got %v", i, err)
		}
	}
	if len(r.received()) != 0 {
		t.Fatal("the API was sent a request")
	}
	log := r.log.String()
	if strings.Count(log, `"kind":"cred.refresh"`) != 1 || !strings.Contains(log, `"ok":false`) {
		t.Fatalf("want exactly one failed refresh recorded:\n%s", log)
	}
	if strings.Contains(log, `"decision":"confirm"`) {
		t.Fatal("a needs-reauth credential asked for confirmation")
	}
	r.noTokens(t)
}
