package keychain

// Acquiring an oauth2 credential (ADR 24 §2): Authorization Code with PKCE,
// the redirect caught by a single-shot listener on loopback. It is a user
// action, run from the command line (nodwyn credential add --oauth2); no
// Scheme primitive and no tool reaches it, so nothing the model does can put
// a consent screen in front of the user.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// Profile is a provider's OAuth2 endpoints and its default scopes, the
// narrowest set that is useful, so the easy path is the least privileged
// one.
type Profile struct {
	Name           string
	AuthEndpoint   string
	TokenEndpoint  string
	RevokeEndpoint string   // "" when the provider has none a client can call
	Scopes         []string // the default scopes, in the profile's short form
	// ScopePrefix is prepended to a scope given by its short name, as
	// Google's are ("drive.file" is https://www.googleapis.com/auth/drive.file).
	ScopePrefix string
	// AuthParams are extra authorization parameters (k=v), such as Google's
	// access_type=offline, without which it issues no refresh token.
	AuthParams []string
	// TokenAuth is "basic" when the client must authenticate to the token
	// endpoint with HTTP Basic rather than in the form.
	TokenAuth string
	// NeedsSecret is set for a provider that issues no token to a client
	// without a secret.
	NeedsSecret bool
}

// The provider profiles that ship (ADR 24 §1). A custom provider names its
// endpoints instead (Custom).
var profiles = map[string]Profile{
	"google": {
		Name:           "google",
		AuthEndpoint:   "https://accounts.google.com/o/oauth2/v2/auth",
		TokenEndpoint:  "https://oauth2.googleapis.com/token",
		RevokeEndpoint: "https://oauth2.googleapis.com/revoke",
		Scopes:         []string{"drive.file"},
		ScopePrefix:    "https://www.googleapis.com/auth/",
		AuthParams:     []string{"access_type=offline", "prompt=consent"},
	},
	"microsoft": {
		Name:          "microsoft",
		AuthEndpoint:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		TokenEndpoint: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		Scopes:        []string{"offline_access", "Files.ReadWrite.AppFolder"},
	},
	"github": {
		Name:          "github",
		AuthEndpoint:  "https://github.com/login/oauth/authorize",
		TokenEndpoint: "https://github.com/login/oauth/access_token",
		Scopes:        []string{"read:user"},
		NeedsSecret:   true,
	},
	"notion": {
		Name:          "notion",
		AuthEndpoint:  "https://api.notion.com/v1/oauth/authorize",
		TokenEndpoint: "https://api.notion.com/v1/oauth/token",
		AuthParams:    []string{"owner=user"},
		TokenAuth:     "basic",
		NeedsSecret:   true,
	},
}

// ProfileNamed returns a shipped provider profile.
func ProfileNamed(name string) (Profile, bool) {
	p, ok := profiles[strings.ToLower(name)]
	return p, ok
}

// ProfileNames lists the shipped provider profiles.
func ProfileNames() []string {
	out := make([]string, 0, len(profiles))
	for name := range profiles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Custom is the profile of a provider nodwyn does not ship, such as an MCP
// server's authorization server: its endpoints, and no default scopes.
func Custom(auth, token, revoke string) Profile {
	return Profile{Name: "custom", AuthEndpoint: auth, TokenEndpoint: token, RevokeEndpoint: revoke}
}

// FullScope is a scope as the provider names it: a short name with the
// profile's prefix, or the scope as given.
func (p Profile) FullScope(s string) string {
	if p.ScopePrefix == "" || strings.ContainsAny(s, ":/") {
		return s
	}
	switch s {
	case "openid", "email", "profile":
		return s
	}
	return p.ScopePrefix + s
}

// Grant is what the user asks a provider for.
type Grant struct {
	Profile      Profile
	ClientID     string
	ClientSecret string   // "" for a public client: PKCE is the protection
	Scopes       []string // nil: the profile's defaults
}

func (g Grant) tokenSet() (tokenSet, error) {
	p := g.Profile
	if g.ClientID == "" {
		return tokenSet{}, errors.New("keychain: a client id is required (register nodwyn as an OAuth client with the provider)")
	}
	if p.NeedsSecret && g.ClientSecret == "" {
		return tokenSet{}, fmt.Errorf("keychain: %s issues no token without a client secret", p.Name)
	}
	endpoints := []string{p.AuthEndpoint, p.TokenEndpoint}
	if p.RevokeEndpoint != "" {
		endpoints = append(endpoints, p.RevokeEndpoint)
	}
	for _, endpoint := range endpoints {
		if err := checkEndpoint(endpoint); err != nil {
			return tokenSet{}, err
		}
	}
	scopes := g.Scopes
	if scopes == nil {
		scopes = p.Scopes
	}
	var full []string
	for _, s := range scopes {
		if s = strings.TrimSpace(s); s != "" {
			full = append(full, p.FullScope(s))
		}
	}
	return tokenSet{
		Provider: p.Name, ClientID: g.ClientID, ClientSecret: g.ClientSecret,
		AuthEndpoint: p.AuthEndpoint, TokenEndpoint: p.TokenEndpoint, RevokeEndpoint: p.RevokeEndpoint,
		TokenAuth: p.TokenAuth, AuthParams: slices.Clone(p.AuthParams), Scopes: full,
	}, nil
}

// checkEndpoint accepts an https URL, or http on a loopback host (a provider
// on this machine, and the tests): a refresh token never crosses a network
// in the clear.
func checkEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("keychain: %q is not an absolute URL", endpoint)
	}
	if u.Scheme == "https" {
		return nil
	}
	if ip := net.ParseIP(u.Hostname()); u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("keychain: %s must be https", endpoint)
}

// authorizeWait is how long the loopback listener waits for the redirect.
const authorizeWait = 5 * time.Minute

// AddOAuth2 authorizes a new oauth2 credential of class with the provider
// and stores it. open is given the authorization URL to show the user (the
// command line opens a browser and prints it).
func (st *Store) AddOAuth2(ctx context.Context, class string, g Grant, open func(string) error) (*scheme.Handle, error) {
	if class == "" {
		return nil, errors.New("keychain: class is required")
	}
	t, err := g.tokenSet()
	if err != nil {
		return nil, err
	}
	if t, err = st.authorize(ctx, t, open); err != nil {
		return nil, err
	}
	return st.add(entry{class: class, created: st.now().UTC(), oauth: &t})
}

// Reauthorize runs the authorization again for the oauth2 credential id,
// with the same client, endpoints and scopes, and replaces its tokens: how a
// credential marked needs-reauth comes back.
func (st *Store) Reauthorize(ctx context.Context, id string, open func(string) error) error {
	st.mu.Lock()
	st.freshLocked()
	e, ok := st.entries[id]
	st.mu.Unlock()
	if !ok {
		return errNotFound
	}
	if e.oauth == nil {
		return fmt.Errorf("keychain: %s is a static credential; there is nothing to authorize", e.class)
	}
	t := *e.oauth
	t.AccessToken, t.RefreshToken, t.ExpiresAt, t.NeedsReauth = "", "", time.Time{}, false
	t, err := st.authorize(ctx, t, open)
	if err != nil {
		return err
	}
	return st.change(func() error {
		cur, ok := st.entries[id]
		if !ok {
			return errNotFound
		}
		cur.oauth = &t
		st.entries[id] = cur
		return nil
	})
}

// authorize runs Authorization Code with PKCE (RFC 7636) for t: a verifier
// and a state nonce, a listener on 127.0.0.1 at a random port that serves
// only /callback and answers one request, the authorization URL handed to
// open, the state checked on the redirect, and the code exchanged with the
// verifier. It returns t with its tokens.
func (st *Store) authorize(ctx context.Context, t tokenSet, open func(string) error) (tokenSet, error) {
	verifier := base64.RawURLEncoding.EncodeToString(random(32))
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := base64.RawURLEncoding.EncodeToString(random(16))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return t, fmt.Errorf("keychain: loopback listener: %w", err)
	}
	redirect := fmt.Sprintf("http://%s/callback", ln.Addr())

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res result
		switch {
		case r.Method != http.MethodGet:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		case q.Get("state") != state:
			res.err = errors.New("the redirect's state does not match this authorization; nothing was stored")
		case q.Get("error") != "":
			res.err = fmt.Errorf("the provider refused: %s %s", q.Get("error"), q.Get("error_description"))
		case q.Get("code") == "":
			res.err = errors.New("the redirect carried no code")
		default:
			res.code = q.Get("code")
		}
		select {
		case done <- res:
		default: // a second request after the first: the flow is over
			http.Error(w, "this authorization is already over", http.StatusGone)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		msg := "nodwyn has the authorization. You can close this tab."
		if res.err != nil {
			w.WriteHeader(http.StatusBadRequest)
			msg = "nodwyn did not store anything: " + res.err.Error()
		}
		fmt.Fprintf(w, "<!doctype html><title>nodwyn</title><p>%s</p>", html.EscapeString(msg))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {t.ClientID},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if len(t.Scopes) > 0 {
		q.Set("scope", strings.Join(t.Scopes, " "))
	}
	for _, kv := range t.AuthParams {
		if k, v, ok := strings.Cut(kv, "="); ok {
			q.Set(k, v)
		}
	}
	authURL := t.AuthEndpoint
	if strings.Contains(authURL, "?") {
		authURL += "&" + q.Encode()
	} else {
		authURL += "?" + q.Encode()
	}
	if err := open(authURL); err != nil {
		return t, err
	}

	wait, cancel := context.WithTimeout(ctx, authorizeWait)
	defer cancel()
	var res result
	select {
	case res = <-done:
	case <-wait.Done():
		return t, fmt.Errorf("keychain: no authorization arrived within %s", authorizeWait)
	}
	if res.err != nil {
		return t, fmt.Errorf("keychain: %w", res.err)
	}
	tok, err := st.tokenRequest(ctx, t, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	})
	if err != nil {
		return t, fmt.Errorf("keychain: exchange the code at %s: %w", hostOf(t.TokenEndpoint), err)
	}
	t.AccessToken, t.RefreshToken = tok.AccessToken, tok.RefreshToken
	t.ExpiresAt = tok.expiry(st.now())
	if granted := splitScopes(tok.Scope); len(granted) > 0 {
		t.Scopes = granted
	}
	t.NeedsReauth = false
	return t, nil
}
