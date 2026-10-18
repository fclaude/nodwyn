package keychain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/scheme"
	fakeoauth "github.com/fclaude/nodwyn/test/fakes/oauth"
)

// clock is a test clock that only moves when told.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func oauthStore(t *testing.T) (*Store, *fakeoauth.Server, *clock) {
	t.Helper()
	srv := fakeoauth.New("nodwyn-test")
	t.Cleanup(srv.Close)
	st := newStore(t, []byte("pass"))
	c := &clock{now: time.Now()}
	st.SetClock(c.Now)
	return st, srv, c
}

func grant(srv *fakeoauth.Server, scopes ...string) Grant {
	return Grant{Profile: Custom(srv.AuthURL(), srv.TokenURL(), srv.RevokeURL()), ClientID: srv.ClientID, Scopes: scopes}
}

func injected(t *testing.T, st *Store, id string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", "https://api.example/", nil)
	if err := st.InjectHeader(id, "Authorization", "Bearer ", req); err != nil {
		t.Fatal(err)
	}
	return req.Header.Get("Authorization")
}

func TestOAuth2AcquireStoresATokenSet(t *testing.T) {
	st, srv, _ := oauthStore(t)
	h, err := st.AddOAuth2(context.Background(), "docs", grant(srv, "files.read", "files.write"), srv.Browser)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Exchanges() != 1 {
		t.Fatalf("one PKCE exchange, got %d", srv.Exchanges())
	}
	info, err := st.Info(h.ID)
	if err != nil || info.Kind != KindOAuth2 || info.Class != "docs" || strings.Join(info.Scopes, " ") != "files.read files.write" || !info.Refreshable || info.Expires.IsZero() {
		t.Fatalf("info %+v %v", info, err)
	}
	if got := injected(t, st, h.ID); !strings.HasPrefix(got, "Bearer fake-access-token-") {
		t.Fatalf("injected %q", got)
	}
	// The handle and the description carry no token.
	if fakeoauth.HasToken(fmt.Sprintf("%+v %+v", h, info)) {
		t.Fatal("a token reached the handle or the info")
	}
	// The file is version 2, and opens again with its token set.
	reopened, err := Open(st.path, []byte("pass"))
	if err != nil {
		t.Fatal(err)
	}
	if got := injected(t, reopened, h.ID); !strings.HasPrefix(got, "Bearer fake-access-token-") {
		t.Fatalf("after reopening: %q", got)
	}
	if v := version(t, st); v != 2 {
		t.Fatalf("a keychain with an oauth2 entry is version 2, got %d", v)
	}
}

func TestStaticOnlyKeychainStaysVersion1(t *testing.T) {
	st := newStore(t, []byte("pass"))
	if _, err := st.Add("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if v := version(t, st); v != 1 {
		t.Fatalf("an older binary must still open a static-only keychain: version %d", v)
	}
}

// version decrypts the file and reads its version.
func version(t *testing.T, st *Store) int {
	t.Helper()
	raw, err := os.ReadFile(st.path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := split(raw)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := st.aead.Open(nil, f.nonce, f.ct, magic)
	if err != nil {
		t.Fatal(err)
	}
	var doc wireDoc
	json.Unmarshal(pt, &doc)
	return doc.Version
}

func TestOAuth2RefreshRotatesAndReports(t *testing.T) {
	st, srv, c := oauthStore(t)
	srv.SetRotate(true)
	h, err := st.AddOAuth2(context.Background(), "docs", grant(srv), srv.Browser)
	if err != nil {
		t.Fatal(err)
	}
	before := injected(t, st, h.ID)
	var seen []Refreshed
	observe := func(r Refreshed) { seen = append(seen, r) }
	// With time left, nothing happens.
	if err := st.Refresh(context.Background(), h.ID, observe); err != nil || len(seen) != 0 || srv.Refreshes() != 0 {
		t.Fatalf("fresh token refreshed: %v %v %d", err, seen, srv.Refreshes())
	}
	// Within the skew of expiry, it is refreshed first.
	c.Add(time.Hour - 30*time.Second)
	if err := st.Refresh(context.Background(), h.ID, observe); err != nil {
		t.Fatal(err)
	}
	if srv.Refreshes() != 1 || len(seen) != 1 || !seen[0].Rotated || seen[0].Err != nil || seen[0].Class != "docs" || seen[0].Endpoint != "127.0.0.1" || !seen[0].Expires.After(c.Now()) {
		t.Fatalf("refresh %d, seen %+v", srv.Refreshes(), seen)
	}
	after := injected(t, st, h.ID)
	if after == before || !strings.HasPrefix(after, "Bearer fake-access-token-") {
		t.Fatalf("access token not replaced: %q -> %q", before, after)
	}
	// The rotated refresh token was saved: refreshing again works, so the
	// old one (retired by the provider) is not the one sent.
	c.Add(time.Hour)
	if err := st.Refresh(context.Background(), h.ID, observe); err != nil {
		t.Fatalf("second refresh with the rotated token: %v", err)
	}
	if fakeoauth.HasToken(fmt.Sprintf("%+v", seen)) {
		t.Fatal("a token reached what the session logs")
	}
}

func TestOAuth2ExpiredTokenIsNeverSentStale(t *testing.T) {
	st, srv, c := oauthStore(t)
	h, err := st.AddOAuth2(context.Background(), "docs", grant(srv), srv.Browser)
	if err != nil {
		t.Fatal(err)
	}
	c.Add(2 * time.Hour)
	req, _ := http.NewRequest("GET", "https://api.example/", nil)
	if err := st.InjectHeader(h.ID, "Authorization", "Bearer ", req); err == nil || req.Header.Get("Authorization") != "" {
		t.Fatalf("an expired token was injected without a refresh: %v", err)
	}
}

// Parallel uses refresh once, and a second process holding the same
// keychain finds the token the first one wrote instead of refreshing (and
// rotating) again.
func TestOAuth2RefreshIsSerializedAcrossProcesses(t *testing.T) {
	st, srv, c := oauthStore(t)
	srv.SetRotate(true)
	h, err := st.AddOAuth2(context.Background(), "docs", grant(srv), srv.Browser)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(st.path, []byte("pass"))
	if err != nil {
		t.Fatal(err)
	}
	other.SetClock(c.Now)
	c.Add(time.Hour)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 10; i++ {
		for _, s := range []*Store{st, other} {
			wg.Add(1)
			go func(s *Store) {
				defer wg.Done()
				errs <- s.Refresh(context.Background(), h.ID, nil)
			}(s)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if srv.Refreshes() != 1 {
		t.Fatalf("want one refresh, got %d", srv.Refreshes())
	}
	if a, b := injected(t, st, h.ID), injected(t, other, h.ID); a != b {
		t.Fatalf("the two processes inject different tokens: %q %q", a, b)
	}
}

// A refresh the provider refuses marks the credential, and every use is
// denied naming the command that fixes it; nothing is asked again until
// the user authorizes, after which it works.
func TestOAuth2RefusedRefreshNeedsReauth(t *testing.T) {
	st, srv, c := oauthStore(t)
	h, err := st.AddOAuth2(context.Background(), "docs", grant(srv), srv.Browser)
	if err != nil {
		t.Fatal(err)
	}
	srv.RevokeAll()
	c.Add(time.Hour)
	var seen []Refreshed
	err = st.Refresh(context.Background(), h.ID, func(r Refreshed) { seen = append(seen, r) })
	if err == nil || !strings.Contains(err.Error(), "nodwyn credential login "+h.ID) {
		t.Fatalf("want a re-authorize denial, got %v", err)
	}
	if len(seen) != 1 || seen[0].Err == nil {
		t.Fatalf("the failed refresh is reported: %+v", seen)
	}
	if info, _ := st.Info(h.ID); !info.NeedsReauth {
		t.Fatal("not marked needs-reauth")
	}
	n := srv.Refreshes()
	if err := st.Refresh(context.Background(), h.ID, nil); err == nil || srv.Refreshes() != n {
		t.Fatal("a needs-reauth credential is not tried again")
	}
	req, _ := http.NewRequest("GET", "https://api.example/", nil)
	if err := st.InjectHeader(h.ID, "Authorization", "Bearer ", req); err == nil {
		t.Fatal("a needs-reauth credential was injected")
	}
	if err := st.Reauthorize(context.Background(), h.ID, srv.Browser); err != nil {
		t.Fatal(err)
	}
	if info, _ := st.Info(h.ID); info.NeedsReauth || !strings.HasPrefix(injected(t, st, h.ID), "Bearer fake-access-token-") {
		t.Fatalf("after login: %+v", info)
	}
}

func TestOAuth2Revoke(t *testing.T) {
	st, srv, _ := oauthStore(t)
	h, err := st.AddOAuth2(context.Background(), "docs", grant(srv), srv.Browser)
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := st.Revoke(context.Background(), h.ID)
	if err != nil || !revoked {
		t.Fatalf("revoke: %v %v", revoked, err)
	}
	if got := srv.Revoked(); len(got) != 1 || !strings.HasPrefix(got[0], "fake-refresh-token-") {
		t.Fatalf("the provider was not asked to revoke the refresh token: %v", got)
	}
	if _, err := st.Info(h.ID); err == nil || st.Count() != 0 {
		t.Fatal("revoked credential still stored")
	}
	// Without a revocation endpoint it is only deleted, and says so.
	g := grant(srv)
	g.Profile.RevokeEndpoint = ""
	h, err = st.AddOAuth2(context.Background(), "docs", g, srv.Browser)
	if err != nil {
		t.Fatal(err)
	}
	if revoked, err := st.Revoke(context.Background(), h.ID); err != nil || revoked {
		t.Fatalf("no endpoint: revoked=%v err=%v", revoked, err)
	}
}

func TestOAuth2AcquireRefusals(t *testing.T) {
	st, srv, _ := oauthStore(t)
	// A redirect with another state is refused, and nothing is stored.
	wrongState := func(authURL string) error {
		go func() {
			u := strings.SplitN(authURL, "redirect_uri=", 2)[1]
			u = strings.SplitN(u, "&", 2)[0]
			cb, _ := urlUnescape(u)
			resp, err := http.Get(cb + "?code=x&state=forged")
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	if _, err := st.AddOAuth2(context.Background(), "docs", grant(srv), wrongState); err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("forged state: %v", err)
	}
	srv.SetDeny(true)
	if _, err := st.AddOAuth2(context.Background(), "docs", grant(srv), srv.Browser); err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("denied: %v", err)
	}
	if st.Count() != 0 {
		t.Fatal("a refused authorization stored something")
	}
	// No client id, and plain http to a remote host, are refused before
	// anything opens.
	if _, err := st.AddOAuth2(context.Background(), "docs", Grant{Profile: Custom(srv.AuthURL(), srv.TokenURL(), "")}, srv.Browser); err == nil {
		t.Fatal("no client id")
	}
	if _, err := st.AddOAuth2(context.Background(), "docs", Grant{Profile: Custom("http://auth.example/a", "http://auth.example/t", ""), ClientID: "c"}, srv.Browser); err == nil {
		t.Fatal("plain http to a remote host")
	}
}

func urlUnescape(s string) (string, error) {
	r := httptest.NewRequest("GET", "/?u="+s, nil)
	return r.URL.Query().Get("u"), nil
}

func TestGoogleProfileExpandsShortScopes(t *testing.T) {
	p, ok := ProfileNamed("google")
	if !ok {
		t.Fatal("no google profile")
	}
	g := Grant{Profile: p, ClientID: "c", Scopes: []string{"drive.file", "openid", "https://www.googleapis.com/auth/documents"}}
	ts, err := g.tokenSet()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ts.Scopes, " ") != "https://www.googleapis.com/auth/drive.file openid https://www.googleapis.com/auth/documents" {
		t.Fatalf("scopes %v", ts.Scopes)
	}
}

func TestDeleteAndList(t *testing.T) {
	st := newStore(t, []byte("pass"))
	a, _ := st.Add("a", []byte("1"))
	b, _ := st.Add("b", []byte("2"))
	if err := st.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	list := st.List()
	if len(list) != 1 || list[0].ID != b.ID || list[0].Kind != KindStatic {
		t.Fatalf("list %+v", list)
	}
	if err := st.Delete(a.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("deleting twice: %v", err)
	}
	// Another process sees the change.
	other, err := Open(st.path, []byte("pass"))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := other.Add("c", []byte("3"))
	if st.HandleForClass("c") == nil || st.HandleForClass("c").ID != c.ID || st.Count() != 2 {
		t.Fatal("a credential another process added is not seen")
	}
}

// A refresh that fails on the way (the token endpoint does not answer) may
// work a moment later: it is transient, so the request that needed it is
// retried, and the credential is not marked for a new sign-in. A refusal
// is not transient.
func TestOAuth2RefreshThatFailsOnTheWayIsTransient(t *testing.T) {
	st, srv, c := oauthStore(t)
	h, err := st.AddOAuth2(context.Background(), "docs", grant(srv), srv.Browser)
	if err != nil {
		t.Fatal(err)
	}
	c.Add(time.Hour)
	srv.Close()
	err = st.Refresh(context.Background(), h.ID, nil)
	if err == nil || !IsTransient(err) {
		t.Fatalf("an endpoint that does not answer: %v (transient %v)", err, IsTransient(err))
	}
	if info, _ := st.Info(h.ID); info.NeedsReauth {
		t.Fatal("a failure on the way asked for a new sign-in")
	}
	if IsTransient(reauthError(h.ID, "docs")) || IsTransient(errNotFound) {
		t.Fatal("a refusal reads as transient")
	}
}

// A browser that cannot reach the listener (it is on another computer than
// the one nodwyn runs on) ends on the provider's redirect to it, and the
// address it ended on finishes the authorization: the same checks as the
// listener's, only this flow's own redirect address, and once.
func TestOAuth2FinishesFromAPastedAddress(t *testing.T) {
	st, srv, _ := oauthStore(t)
	var finish func(string) error
	ctx := WithFinish(context.Background(), func(f func(string) error) { finish = f })
	var ended string
	asked := make(chan struct{})
	// The browser follows nothing: it stops at the redirect, as one whose
	// loopback is not the daemon's does, and the person copies its address.
	browser := func(authURL string) error {
		go func() {
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err := client.Get(authURL)
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			ended = resp.Header.Get("Location")
			close(asked)
		}()
		return nil
	}
	type outcome struct {
		h   *scheme.Handle
		err error
	}
	got := make(chan outcome, 1)
	go func() {
		h, err := st.AddOAuth2(ctx, "docs", grant(srv, "files.read"), browser)
		got <- outcome{h, err}
	}()
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser never reached the redirect")
	}
	u, err := url.Parse(ended)
	if err != nil || u.Host == "" {
		t.Fatalf("redirect %q %v", ended, err)
	}
	// What is not this flow's own redirect is refused, and the flow goes on.
	for _, bad := range []string{"", "not a url", "https://" + u.Host + u.Path + "?" + u.RawQuery, "http://127.0.0.1:1" + u.Path + "?" + u.RawQuery, "http://" + u.Host + "/other?" + u.RawQuery, "http://user@" + u.Host + u.Path + "?" + u.RawQuery} {
		if err := finish(bad); err == nil || !strings.Contains(err.Error(), "not the address") {
			t.Fatalf("finish(%q): %v", bad, err)
		}
	}
	select {
	case o := <-got:
		t.Fatalf("a refused address ended the flow: %+v", o)
	default:
	}
	if err := finish("  " + ended + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := finish(ended); err == nil || !strings.Contains(err.Error(), "already over") {
		t.Fatalf("a second address: %v", err)
	}
	o := <-got
	if o.err != nil {
		t.Fatal(o.err)
	}
	if srv.Exchanges() != 1 {
		t.Fatalf("one PKCE exchange, got %d", srv.Exchanges())
	}
	if got := injected(t, st, o.h.ID); !strings.HasPrefix(got, "Bearer fake-access-token-") {
		t.Fatalf("injected %q", got)
	}
	// An address with another state stores nothing and ends the flow, as
	// the listener's handler does.
	var forged func(string) error
	ctx = WithFinish(context.Background(), func(f func(string) error) { forged = f })
	redirects := make(chan string, 1)
	capture := func(authURL string) error {
		redirect, _ := urlUnescape(strings.SplitN(strings.SplitN(authURL, "redirect_uri=", 2)[1], "&", 2)[0])
		redirects <- redirect
		return nil
	}
	errs := make(chan error, 1)
	count := st.Count()
	go func() { _, err := st.AddOAuth2(ctx, "other", grant(srv), capture); errs <- err }()
	redirect := <-redirects
	if err := forged(redirect + "?code=x&state=forged"); err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("forged state: %v", err)
	}
	if err := <-errs; err == nil || st.Count() != count {
		t.Fatalf("a forged state stored something: %v", err)
	}
}
