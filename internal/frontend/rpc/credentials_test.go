package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	fakeoauth "github.com/fclaude/nodwyn/test/fakes/oauth"
)

// A keychain created from the console without a passphrase opens by
// itself; one can be given later, and only the current passphrase removes
// it again.
func TestKeychainWithoutPassphrase(t *testing.T) {
	o := options(t)
	var status keychainStatus
	decode := func(raw json.RawMessage) { status = keychainStatus{}; json.Unmarshal(raw, &status) }
	path, stop := start(t, o)
	c := connect(t, path)
	c.hello()
	if r := c.call("keychain.create", map[string]any{"passphrase": "correct horse", "no_passphrase": true}); r.Error == nil {
		t.Fatal("accepted a passphrase and none")
	}
	if r := c.call("keychain.create", map[string]any{}); r.Error == nil {
		t.Fatal("created without a passphrase nobody asked to leave out")
	}
	decode(c.ok("keychain.create", map[string]any{"no_passphrase": true}))
	if !status.Unlocked || !status.NoPassphrase {
		t.Fatalf("created without a passphrase: %+v", status)
	}
	if _, err := keychain.OpenWithoutPassphrase(o.Paths.Keychain); err != nil {
		t.Fatal(err)
	}
	stop()

	// A daemon started without it opens it with an empty passphrase.
	path, stop = start(t, o)
	c = connect(t, path)
	c.hello()
	decode(c.ok("keychain.unlock", map[string]any{"passphrase": ""}))
	if !status.Unlocked || !status.NoPassphrase {
		t.Fatalf("unlocked: %+v", status)
	}
	if r := c.call("keychain.passphrase", map[string]any{"passphrase": "short"}); r.Error == nil {
		t.Fatal("accepted a short passphrase")
	}
	if r := c.call("keychain.passphrase", map[string]any{"no_passphrase": true}); r.Error == nil {
		t.Fatal("removed a passphrase it does not have")
	}
	decode(c.ok("keychain.passphrase", map[string]any{"passphrase": "correct horse"}))
	if status.NoPassphrase || !status.Unlocked {
		t.Fatalf("after setting one: %+v", status)
	}
	if _, err := keychain.OpenWithoutPassphrase(o.Paths.Keychain); err != keychain.ErrNeedsPassphrase {
		t.Fatalf("on disk after setting one: %v", err)
	}
	if r := c.call("keychain.passphrase", map[string]any{"no_passphrase": true}); r.Error == nil {
		t.Fatal("removed the passphrase without the current one")
	}
	if r := c.call("keychain.passphrase", map[string]any{"current": "wrong horse", "no_passphrase": true}); r.Error == nil || strings.Contains(r.Error.Message, "horse") {
		t.Fatalf("removed with the wrong passphrase: %+v", r.Error)
	}
	decode(c.ok("keychain.passphrase", map[string]any{"current": "correct horse", "passphrase": "battery staple"}))
	if status.NoPassphrase {
		t.Fatalf("after changing it: %+v", status)
	}
	decode(c.ok("keychain.passphrase", map[string]any{"current": "battery staple", "no_passphrase": true}))
	if !status.NoPassphrase {
		t.Fatalf("after removing it: %+v", status)
	}
	stop()
}

// Credentials are added, allowed for a host, replaced and removed from the
// console; the list says where each may go and what uses it, and never
// carries a value.
func TestCredentialsAdminOverTheSocket(t *testing.T) {
	o := options(t)
	o.Config.Control = config.Model{Provider: "openai", URL: "https://api.example.com/v1", Model: "m", Credential: "model-key"}
	os.MkdirAll(o.Paths.ConfigDir, 0o700)
	os.WriteFile(config.MCPFile(o.Paths), []byte("[[server]]\nname = \"notes\"\nurl = \"https://mcp.example.com/mcp\"\ncredential = \"notes-key\"\nposition = \"header:Authorization:Bearer\"\n"), 0o600)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	if r := c.call("credentials.add", map[string]any{"class": "github-token", "value": "ghp-canary-1"}); r.Error == nil {
		t.Fatal("added a key to a locked keychain")
	}
	var list credentialsResult
	json.Unmarshal(c.ok("credentials.list", map[string]any{}), &list)
	if list.Keychain.Unlocked || len(list.Credentials) != 0 || len(list.Providers) == 0 {
		t.Fatalf("locked: %+v", list)
	}
	c.ok("keychain.create", map[string]any{"passphrase": "correct horse"})

	for _, bad := range []map[string]any{
		{"class": "has space", "value": "x"},
		{"class": "github-token", "value": ""},
		{"class": "github-token", "value": "x", "dest": "https://api.github.com/", "methods": []string{"GET"}, "position": "header:Authorization:Bearer"},
		{"class": "github-token", "value": "x", "dest": "api.github.com"},
	} {
		if r := c.call("credentials.add", bad); r.Error == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	if _, err := os.Stat(o.Paths.Bindings); err == nil {
		t.Fatal("a refused add wrote bindings")
	}
	json.Unmarshal(c.ok("credentials.add", map[string]any{"class": "github-token", "value": "ghp-canary-1", "dest": "API.github.com", "methods": []string{"get", "POST"}, "position": "header:Authorization:Bearer"}), &list)
	if len(list.Credentials) != 1 {
		t.Fatalf("after add: %+v", list.Credentials)
	}
	gh := list.Credentials[0]
	if gh.Class != "github-token" || gh.Kind != keychain.KindStatic || len(gh.Bindings) != 2 || gh.Bindings[0].Dest != "api.github.com" || gh.Bindings[0].Methods[0] != "GET" {
		t.Fatalf("added: %+v", gh)
	}
	table, err := binding.Load(o.Paths.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := table.Lookup("github-token", "api.github.com", "POST", "header:Authorization:Bearer"); !ok {
		t.Fatalf("bindings.toml: %+v", table)
	}
	if r := c.call("credentials.add", map[string]any{"class": "github-token", "value": "ghp-canary-2"}); r.Error == nil {
		t.Fatal("added a second credential of the same class")
	}
	json.Unmarshal(c.ok("credentials.add", map[string]any{"class": "model-key", "value": "sk-canary-3"}), &list)
	json.Unmarshal(c.ok("credentials.bind", map[string]any{"id": gh.ID, "dest": "uploads.github.com", "methods": []string{"PUT"}, "position": "header:Authorization:Bearer"}), &list)
	var model credentialInfo
	for _, cred := range list.Credentials {
		switch cred.Class {
		case "github-token":
			gh = cred
		case "model-key":
			model = cred
		}
	}
	if len(gh.Bindings) != 3 || !slices.Equal(model.UsedBy, []string{"the main model"}) || len(model.Bindings) != 0 {
		t.Fatalf("listed: %+v %+v", gh, model)
	}
	if len(list.Missing) != 1 || list.Missing[0].Class != "notes-key" || !slices.Equal(list.Missing[0].UsedBy, []string{"MCP server notes"}) {
		t.Fatalf("missing: %+v", list.Missing)
	}

	// A host it was allowed for can be taken back, whatever its methods:
	// narrowing is the safe direction, and bindings.toml keeps the rest.
	if r := c.call("credentials.unbind", map[string]any{"id": gh.ID, "dest": "nowhere.example", "positions": []string{"header:Authorization:Bearer"}}); r.Error == nil || !strings.Contains(r.Error.Message, "no row allowing") {
		t.Fatalf("took back a row that is not there: %+v", r.Error)
	}
	json.Unmarshal(c.ok("credentials.unbind", map[string]any{"id": gh.ID, "dest": "uploads.github.com", "positions": []string{"header:Authorization:Bearer"}}), &list)
	for _, cred := range list.Credentials {
		if cred.Class == "github-token" {
			gh = cred
		}
	}
	if len(gh.Bindings) != 2 || gh.Bindings[0].Dest != "api.github.com" || gh.Bindings[1].Dest != "api.github.com" {
		t.Fatalf("after unbinding uploads.github.com: %+v", gh.Bindings)
	}
	table, err = binding.Load(o.Paths.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := table.Lookup("github-token", "uploads.github.com", "PUT", "header:Authorization:Bearer"); ok {
		t.Fatal("the host taken back is still bound")
	}
	if ok, _ := table.Lookup("github-token", "api.github.com", "POST", "header:Authorization:Bearer"); !ok {
		t.Fatal("another host's row went with it")
	}
	// Put back, so what follows sees the three rows it was written for.
	json.Unmarshal(c.ok("credentials.bind", map[string]any{"id": gh.ID, "dest": "uploads.github.com", "methods": []string{"PUT"}, "position": "header:Authorization:Bearer"}), &list)

	c.ok("credentials.replace", map[string]any{"id": gh.ID, "value": "ghp-canary-4"})
	store, err := keychain.Open(o.Paths.Keychain, []byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err := store.InjectHeader(gh.ID, "Authorization", "Bearer ", req); err != nil || req.Header.Get("Authorization") != "Bearer ghp-canary-4" {
		t.Fatalf("replaced: %v %q", err, req.Header.Get("Authorization"))
	}

	var removed credentialRemoved
	json.Unmarshal(c.ok("credentials.remove", map[string]any{"id": gh.ID, "revoke": true}), &removed)
	if removed.Revoked || len(removed.List.Credentials) != 1 {
		t.Fatalf("removed: %+v", removed)
	}
	if r := c.call("credentials.remove", map[string]any{"id": gh.ID}); r.Error == nil {
		t.Fatal("removed twice")
	}
	for _, r := range c.observed {
		if strings.Contains(string(r.Result), "canary") || r.Error != nil && strings.Contains(r.Error.Message, "canary") {
			t.Fatalf("a value came back: %s %+v", r.Result, r.Error)
		}
	}
}

// An OAuth2 credential is authorized from the console: the daemon answers
// with the provider's consent page at once, the provider sends the browser
// back to the daemon's loopback listener, and the list shows the outcome.
// Authorizing it again replaces its tokens; removing it revokes them.
func TestCredentialsAuthorizeOAuth2(t *testing.T) {
	provider := fakeoauth.New("console-client")
	defer provider.Close()
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("keychain.create", map[string]any{"no_passphrase": true})
	if r := c.call("credentials.authorize", map[string]any{"provider": "nowhere", "client_id": "x"}); r.Error == nil {
		t.Fatal("authorized with an unknown provider")
	}
	if r := c.call("credentials.authorize", map[string]any{"provider": "custom", "client_id": "", "auth_url": provider.AuthURL(), "token_url": provider.TokenURL()}); r.Error == nil {
		t.Fatal("authorized without a client id")
	}
	var started authorizationInfo
	json.Unmarshal(c.ok("credentials.authorize", map[string]any{"provider": "custom", "class": "drive", "client_id": provider.ClientID,
		"auth_url": provider.AuthURL(), "token_url": provider.TokenURL(), "revoke_url": provider.RevokeURL(), "scopes": []string{"files"}}), &started)
	if started.State != "waiting" || !strings.HasPrefix(started.URL, provider.AuthURL()+"?") || started.Class != "drive" {
		t.Fatalf("started: %+v", started)
	}
	provider.Browser(started.URL)
	list := waitAuthorization(t, c, started.ID)
	if len(list.Credentials) != 1 {
		t.Fatalf("authorized: %+v", list)
	}
	drive := list.Credentials[0]
	if drive.Kind != keychain.KindOAuth2 || drive.Class != "drive" || !slices.Equal(drive.Scopes, []string{"files"}) || !drive.Refreshable {
		t.Fatalf("credential: %+v", drive)
	}
	// Allowed for a host, it carries the scopes it was granted.
	json.Unmarshal(c.ok("credentials.bind", map[string]any{"id": drive.ID, "dest": "api.example.com", "methods": []string{"GET"}, "position": "header:Authorization:Bearer"}), &list)
	if b := list.Credentials[0].Bindings; len(b) != 1 || !slices.Equal(b[0].Scopes, []string{"files"}) {
		t.Fatalf("bound: %+v", b)
	}
	if r := c.call("credentials.replace", map[string]any{"id": drive.ID, "value": "x"}); r.Error == nil {
		t.Fatal("replaced an oauth2 credential's value")
	}

	started = authorizationInfo{}
	json.Unmarshal(c.ok("credentials.authorize", map[string]any{"id": drive.ID}), &started)
	provider.Browser(started.URL)
	waitAuthorization(t, c, started.ID)
	if provider.Exchanges() != 2 {
		t.Fatalf("exchanges: %d", provider.Exchanges())
	}

	provider.SetDeny(true)
	started = authorizationInfo{}
	json.Unmarshal(c.ok("credentials.authorize", map[string]any{"id": drive.ID}), &started)
	provider.Browser(started.URL)
	deadline := time.Now().Add(5 * time.Second)
	for {
		list = credentialsResult{}
		json.Unmarshal(c.ok("credentials.list", map[string]any{}), &list)
		if a := find(list.Authorizations, started.ID); a.State == "failed" {
			if !strings.Contains(a.Error, "access_denied") || a.URL != "" {
				t.Fatalf("refused: %+v", a)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a refused authorization never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}

	var removed credentialRemoved
	json.Unmarshal(c.ok("credentials.remove", map[string]any{"id": drive.ID, "revoke": true}), &removed)
	if !removed.Revoked || len(removed.List.Credentials) != 0 || len(provider.Revoked()) == 0 {
		t.Fatalf("revoked: %+v, provider %v", removed, provider.Revoked())
	}
}

// waitAuthorization polls the list as the console does until the
// authorization id is done.
func waitAuthorization(t *testing.T, c *client, id string) credentialsResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var list credentialsResult
		json.Unmarshal(c.ok("credentials.list", map[string]any{}), &list)
		a := find(list.Authorizations, id)
		switch a.State {
		case "done":
			if a.URL != "" || a.Credential == "" {
				t.Fatalf("done: %+v", a)
			}
			return list
		case "failed":
			t.Fatalf("failed: %+v", a)
		}
		if time.Now().After(deadline) {
			t.Fatalf("authorization %s never finished: %+v", id, list.Authorizations)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func find(list []authorizationInfo, id string) authorizationInfo {
	for _, a := range list {
		if a.ID == id {
			return a
		}
	}
	return authorizationInfo{}
}

// A keychain whose passphrase another process changed shows as locked and
// opens again, in place, with the new passphrase (P2-003): the daemon would
// otherwise be unable to add a key or refresh a token until restarted.
func TestKeychainOpensAgainAfterItsPassphraseChangedElsewhere(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("keychain.create", map[string]any{"passphrase": "correct horse"})
	if r := c.call("keychain.unlock", map[string]any{"passphrase": "correct horse"}); r.Error == nil {
		t.Fatal("unlocked a keychain that is open and current")
	}
	other, err := keychain.Open(o.Paths.Keychain, []byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	if err := other.SetPassphrase([]byte("battery staple")); err != nil {
		t.Fatal(err)
	}
	var status keychainStatus
	json.Unmarshal(c.ok("keychain.status", map[string]any{}), &status)
	if !status.Stale || status.Unlocked {
		t.Fatalf("after the passphrase changed elsewhere: %+v", status)
	}
	if r := c.call("credentials.add", map[string]any{"class": "github-token", "value": "x"}); r.Error == nil {
		t.Fatal("added a key through a stale keychain")
	}
	if r := c.call("keychain.unlock", map[string]any{"passphrase": "correct horse"}); r.Error == nil {
		t.Fatal("the old passphrase unlocked it")
	}
	status = keychainStatus{}
	json.Unmarshal(c.ok("keychain.unlock", map[string]any{"passphrase": "battery staple"}), &status)
	if status.Stale || !status.Unlocked {
		t.Fatalf("after unlocking again: %+v", status)
	}
	c.ok("credentials.add", map[string]any{"class": "github-token", "value": "x"})
}

// A provider that is slow to answer a revocation holds up that one call, not
// the daemon: another client's methods are answered meanwhile (P2-005).
func TestSlowRevocationDoesNotHoldTheDaemon(t *testing.T) {
	provider := fakeoauth.New("console-client")
	defer provider.Close()
	asked, release := make(chan struct{}), make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(asked)
		<-release
	}))
	defer slow.Close()
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("keychain.create", map[string]any{"no_passphrase": true})
	var started authorizationInfo
	json.Unmarshal(c.ok("credentials.authorize", map[string]any{"provider": "custom", "class": "drive", "client_id": provider.ClientID,
		"auth_url": provider.AuthURL(), "token_url": provider.TokenURL(), "revoke_url": slow.URL + "/revoke", "scopes": []string{"files"}}), &started)
	provider.Browser(started.URL)
	list := waitAuthorization(t, c, started.ID)
	id := list.Credentials[0].ID

	removed := make(chan json.RawMessage, 1)
	go func() { removed <- c.ok("credentials.remove", map[string]any{"id": id, "revoke": true}) }()
	<-asked
	other := connect(t, path)
	other.hello()
	answered := make(chan struct{})
	go func() {
		other.ok("keychain.status", map[string]any{})
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon answered no one while a revocation was pending")
	}
	unblock()
	<-removed
}

// What a credential is allowed for can be taken back from the console, one
// host at a time, and goes with the credential when it is removed: a mistaken
// click on "Allow it for a host" is no one-way ratchet, and the next
// credential that takes the class does not inherit its authority (P2-012).
func TestCredentialsCanBeUnboundAndTheirRowsGoWithThem(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("keychain.create", map[string]any{"passphrase": "correct horse"})
	var list credentialsResult
	json.Unmarshal(c.ok("credentials.add", map[string]any{"class": "github-token", "value": "ghp-canary-1", "dest": "api.github.com", "methods": []string{"GET", "POST"}, "position": "header:Authorization:Bearer"}), &list)
	id := list.Credentials[0].ID
	json.Unmarshal(c.ok("credentials.bind", map[string]any{"id": id, "dest": "uploads.github.com", "methods": []string{"PUT"}, "position": "header:Authorization:Bearer"}), &list)
	if len(list.Credentials[0].Bindings) != 3 {
		t.Fatalf("bound: %+v", list.Credentials[0].Bindings)
	}
	if r := c.call("credentials.unbind", map[string]any{"id": id, "dest": "elsewhere.example", "positions": []string{"header:Authorization:Bearer"}}); r.Error == nil {
		t.Fatal("unbound a host it was never allowed for")
	}
	json.Unmarshal(c.ok("credentials.unbind", map[string]any{"id": id, "dest": "API.github.com", "positions": []string{"header:Authorization:Bearer"}}), &list)
	if rows := list.Credentials[0].Bindings; len(rows) != 1 || rows[0].Dest != "uploads.github.com" {
		t.Fatalf("after unbinding api.github.com: %+v", rows)
	}
	table, err := binding.Load(o.Paths.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := table.Lookup("github-token", "api.github.com", "GET", "header:Authorization:Bearer"); ok {
		t.Fatal("the daemon still sends the key to a host it was unbound from")
	}
	c.ok("credentials.remove", map[string]any{"id": id})
	if table, _ := binding.Load(o.Paths.Bindings); len(table.Bind) != 0 {
		t.Fatalf("a removed credential's rows stayed: %+v", table.Bind)
	}
	json.Unmarshal(c.ok("credentials.add", map[string]any{"class": "github-token", "value": "ghp-canary-2"}), &list)
	if len(list.Credentials[0].Bindings) != 0 {
		t.Fatalf("the next credential of the class inherited %+v", list.Credentials[0].Bindings)
	}
}

// A sign-in is finished from the address the person's browser ended on when
// it is not on the computer nodwyn runs on, so it cannot reach the listener
// the provider redirects to (ADR 24, amended 2026-10-17): an address that
// is not the flow's own is refused and the flow goes on, the right one
// finishes it, and a second finds it over.
func TestCredentialsAuthorizeFinishesFromAPastedAddress(t *testing.T) {
	provider := fakeoauth.New("console-client")
	defer provider.Close()
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("keychain.create", map[string]any{"no_passphrase": true})
	var started authorizationInfo
	json.Unmarshal(c.ok("credentials.authorize", map[string]any{"provider": "custom", "class": "drive", "client_id": provider.ClientID,
		"auth_url": provider.AuthURL(), "token_url": provider.TokenURL(), "scopes": []string{"files"}}), &started)
	// The browser stops at the provider's redirect to the loopback listener.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(started.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	ended := resp.Header.Get("Location")
	if !strings.HasPrefix(ended, "http://127.0.0.1:") {
		t.Fatalf("redirect %q", ended)
	}
	if r := c.call("credentials.authorize.finish", map[string]any{"id": started.ID, "url": "http://127.0.0.1:1/callback?code=x&state=y"}); r.Error == nil || !strings.Contains(r.Error.Message, "not the address") {
		t.Fatalf("another address: %+v", r.Error)
	}
	if r := c.call("credentials.authorize.finish", map[string]any{"id": "auth-nope", "url": ended}); r.Error == nil || !strings.Contains(r.Error.Message, "not waiting") {
		t.Fatalf("another authorization: %+v", r.Error)
	}
	if r := c.call("credentials.authorize.finish", map[string]any{"id": started.ID, "url": ended, "extra": 1}); r.Error == nil {
		t.Fatal("an unknown field was taken")
	}
	c.ok("credentials.authorize.finish", map[string]any{"id": started.ID, "url": ended})
	list := waitAuthorization(t, c, started.ID)
	if len(list.Credentials) != 1 || list.Credentials[0].Class != "drive" || provider.Exchanges() != 1 {
		t.Fatalf("authorized: %+v, %d exchanges", list, provider.Exchanges())
	}
	if r := c.call("credentials.authorize.finish", map[string]any{"id": started.ID, "url": ended}); r.Error == nil {
		t.Fatal("a finished authorization took a second address")
	}
	for _, r := range c.observed {
		if strings.Contains(string(r.Result), "code=") {
			t.Fatalf("an authorization code came back: %s", r.Result)
		}
	}
}
