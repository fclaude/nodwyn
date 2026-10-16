package keychain

// OAuth2 token sets (ADR 24): what an oauth2 credential holds, how it is
// refreshed, and how it is revoked. Tokens never leave the package except
// as the access token an Inject* sink writes into a request, and the refresh
// and revocation requests this file sends to the endpoints fixed in the
// token set when the user authorized it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// tokenSet is an oauth2 credential: the client it was issued to, the
// endpoints it belongs to, the scopes it was granted, and the tokens. It
// has no String method; the access, refresh and client secrets are read
// only by this package.
type tokenSet struct {
	Provider       string    `json:"provider"`
	ClientID       string    `json:"client_id"`
	ClientSecret   string    `json:"client_secret,omitempty"`
	AuthEndpoint   string    `json:"auth_endpoint"`
	TokenEndpoint  string    `json:"token_endpoint"`
	RevokeEndpoint string    `json:"revoke_endpoint,omitempty"`
	TokenAuth      string    `json:"token_auth,omitempty"`  // "basic", or "" to send the client in the form
	AuthParams     []string  `json:"auth_params,omitempty"` // extra authorization parameters, k=v
	Scopes         []string  `json:"scopes,omitempty"`      // granted
	AccessToken    string    `json:"access_token"`
	RefreshToken   string    `json:"refresh_token,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	Subject        string    `json:"subject,omitempty"`
	HostID         string    `json:"ext_agent_host_id,omitempty"`
	NeedsReauth    bool      `json:"needs_reauth,omitempty"`
}

// refreshSkew is how long before expiry an access token is refreshed, so a
// token is never sent in its last seconds.
const refreshSkew = 60 * time.Second

// defaultClient sends refreshes, revocations and code exchanges. It follows
// no redirect: the endpoint was fixed when the user authorized, and a
// refresh token must not go on to a host the server names.
var defaultClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// SetHTTPClient replaces the client of OAuth2 requests (tests).
func (st *Store) SetHTTPClient(c *http.Client) { st.client = c }

// SetClock replaces the clock that decides when a token is stale (tests).
func (st *Store) SetClock(now func() time.Time) { st.now = now }

// Refreshed is one refresh of an oauth2 credential, as the session logs it
// (cred.refresh): never a token.
type Refreshed struct {
	ID       string
	Class    string
	Endpoint string    // the token endpoint's host
	Expires  time.Time // the new access token's expiry, when it succeeded
	Rotated  bool      // the provider issued a new refresh token
	Err      error
}

// reauthError is the denial for a credential whose refresh failed for good.
func reauthError(id, class string) error {
	return fmt.Errorf("keychain: the %s credential (%s) needs authorizing again: run `nodwyn credential login %s`", class, id, id)
}

// Refresh makes the oauth2 credential id fresh: if its access token expires
// within a minute it is refreshed at the token endpoint now, and observe
// (when not nil) is told how that went. A static credential, or a token
// with time left, needs nothing. Refreshes of one credential are
// serialized, across processes too: the second finds the token the first
// wrote. A refresh the provider refuses (the refresh token revoked or
// expired) marks the credential needs-reauth, and every use is denied until
// the user authorizes again; nodwyn never opens a browser for it.
//
// The caller has already checked that the credential may be used where it
// is going: a refresh is part of that use, authorized by the same binding
// row (ADR 24 §3), and goes only to the endpoint fixed in the token set.
func (st *Store) Refresh(ctx context.Context, id string, observe func(Refreshed)) error {
	st.mu.Lock()
	st.freshLocked()
	e, ok := st.entries[id]
	st.mu.Unlock()
	if !ok {
		return errNotFound
	}
	if e.oauth == nil {
		return nil
	}
	if e.oauth.NeedsReauth {
		return reauthError(id, e.class)
	}
	if !st.stale(e.oauth) {
		return nil
	}
	unlock, err := st.lock()
	if err != nil {
		return err
	}
	defer unlock()
	st.mu.Lock()
	err = st.reloadLocked()
	e, ok = st.entries[id]
	st.mu.Unlock()
	switch {
	case err != nil:
		return err
	case !ok:
		return errNotFound
	case e.oauth == nil:
		return nil
	case e.oauth.NeedsReauth:
		return reauthError(id, e.class)
	case !st.stale(e.oauth):
		return nil // another process or goroutine refreshed it
	}
	t := *e.oauth
	r := Refreshed{ID: id, Class: e.class, Endpoint: hostOf(t.TokenEndpoint)}
	report := func() {
		if observe != nil {
			observe(r)
		}
	}
	if t.RefreshToken == "" {
		r.Err = errors.New("the access token expired and there is no refresh token")
		st.markReauth(id)
		report()
		return reauthError(id, e.class)
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}}
	tok, err := st.tokenRequest(ctx, t, form)
	if err != nil {
		r.Err = err
		var refused *refusedError
		if errors.As(err, &refused) {
			st.markReauth(id)
			report()
			return reauthError(id, e.class)
		}
		report()
		return &transientError{fmt.Errorf("keychain: refresh %s: %w", e.class, err)}
	}
	if t.Provider == "openai" {
		if err := checkOpenAIGrant(tok, true); err != nil {
			st.markReauth(id)
			r.Err = err
			report()
			return reauthError(id, e.class)
		}
	}
	t.AccessToken = tok.AccessToken
	t.ExpiresAt = tok.expiry(st.now())
	if tok.RefreshToken != "" && tok.RefreshToken != t.RefreshToken {
		t.RefreshToken, r.Rotated = tok.RefreshToken, true
	}
	if granted := splitScopes(tok.Scope); len(granted) > 0 {
		t.Scopes = granted
	}
	st.mu.Lock()
	cur, ok := st.entries[id]
	if ok {
		cur.oauth = &t
		st.entries[id] = cur
		err = st.save()
		// A set that was not saved stays in memory, and a reload puts it
		// back: the file's refresh token may be retired by now.
		var published *publishedError
		if err != nil && !errors.As(err, &published) {
			if st.pending == nil {
				st.pending = map[string]pendingToken{}
			}
			st.pending[id] = pendingToken{from: e.oauth.RefreshToken, set: t}
		}
	}
	st.mu.Unlock()
	if !ok {
		return errNotFound
	}
	if err != nil {
		r.Err = err
		report()
		return err
	}
	r.Expires = t.ExpiresAt
	report()
	return nil
}

// stale reports whether t's access token must be refreshed before use.
func (st *Store) stale(t *tokenSet) bool {
	return !t.ExpiresAt.IsZero() && !st.now().Add(refreshSkew).Before(t.ExpiresAt)
}

// markReauth records that id's refresh failed for good. The caller holds
// the file lock.
func (st *Store) markReauth(id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if e, ok := st.entries[id]; ok && e.oauth != nil {
		t := *e.oauth
		t.NeedsReauth = true
		e.oauth = &t
		st.entries[id] = e
		_ = st.save()
	}
}

// Revoke revokes an oauth2 credential at its provider, when the provider
// has a revocation endpoint, and deletes it here. revoked reports whether
// the provider was asked: without an endpoint the tokens stay valid there
// until they expire or the user revokes the app in the provider's settings.
// A static credential is only deleted; its service is not told.
func (st *Store) Revoke(ctx context.Context, id string) (revoked bool, err error) {
	st.mu.Lock()
	st.freshLocked()
	e, ok := st.entries[id]
	st.mu.Unlock()
	if !ok {
		return false, errNotFound
	}
	if t := e.oauth; t != nil && t.RevokeEndpoint != "" {
		token, hint := t.RefreshToken, "refresh_token"
		if token == "" {
			token, hint = t.AccessToken, "access_token"
		}
		if err := st.revokeRequest(ctx, *t, token, hint); err != nil {
			return false, fmt.Errorf("keychain: revoke %s at %s: %w (the credential is kept; delete it with --local to drop it anyway)", e.class, hostOf(t.RevokeEndpoint), err)
		}
		revoked = true
	}
	return revoked, st.Delete(id)
}

// token is a token endpoint's answer (RFC 6749 §5.1).
type token struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token"`
}

func (t token) expiry(now time.Time) time.Time {
	if t.ExpiresIn <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(t.ExpiresIn) * time.Second).UTC()
}

// refusedError is a token endpoint saying no for good (RFC 6749 §5.2:
// invalid_grant, invalid_client, unauthorized_client): authorizing again is
// the only way on.
// transientError is a refresh that failed on the way (a timeout, a dropped
// connection, a server error), not one the provider refused: asked again a
// moment later, it may well work.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// IsTransient reports whether err is a refresh that failed on the way and
// may work if tried again; every other credential error stays until a
// person acts.
func IsTransient(err error) bool {
	var t *transientError
	return errors.As(err, &t)
}

type refusedError struct{ code string }

func (e *refusedError) Error() string { return "the provider refused the grant (" + e.code + ")" }

// tokenRequest posts form to t's token endpoint, authenticating as t's
// client, and decodes the answer.
func (st *Store) tokenRequest(ctx context.Context, t tokenSet, form url.Values) (token, error) {
	req, err := st.clientRequest(ctx, t, t.TokenEndpoint, form)
	if err != nil {
		return token{}, err
	}
	resp, err := st.client.Do(req)
	if err != nil {
		return token{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return token{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var oe struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &oe)
		switch oe.Error {
		case "invalid_grant", "invalid_client", "unauthorized_client":
			return token{}, &refusedError{code: oe.Error}
		}
		if oe.Error != "" {
			return token{}, fmt.Errorf("%s: %s", resp.Status, oe.Error)
		}
		return token{}, errors.New(resp.Status)
	}
	var tok token
	if err := json.Unmarshal(raw, &tok); err != nil {
		// GitHub answers form-encoded unless asked for JSON; it is asked.
		return token{}, fmt.Errorf("token endpoint: not JSON: %w", err)
	}
	if tok.AccessToken == "" {
		var oe struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &oe) == nil && oe.Error != "" {
			// GitHub reports errors with a 200.
			if oe.Error == "bad_verification_code" || oe.Error == "invalid_grant" {
				return token{}, &refusedError{code: oe.Error}
			}
			return token{}, errors.New(oe.Error)
		}
		return token{}, errors.New("token endpoint: no access_token in the answer")
	}
	return tok, nil
}

// revokeRequest asks t's revocation endpoint to revoke token (RFC 7009).
func (st *Store) revokeRequest(ctx context.Context, t tokenSet, tok, hint string) error {
	req, err := st.clientRequest(ctx, t, t.RevokeEndpoint, url.Values{"token": {tok}, "token_type_hint": {hint}})
	if err != nil {
		return err
	}
	resp, err := st.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New(resp.Status)
	}
	return nil
}

// clientRequest is a form POST to endpoint authenticated as t's client: in
// the form, or with HTTP Basic for a provider that requires it.
func (st *Store) clientRequest(ctx context.Context, t tokenSet, endpoint string, form url.Values) (*http.Request, error) {
	if t.Provider == "openai" && endpoint == t.TokenEndpoint {
		form.Set("resource", openAIResource)
	}
	if t.TokenAuth != "basic" {
		form.Set("client_id", t.ClientID)
		if t.ClientSecret != "" {
			form.Set("client_secret", t.ClientSecret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if t.TokenAuth == "basic" {
		req.SetBasicAuth(url.QueryEscape(t.ClientID), url.QueryEscape(t.ClientSecret))
	}
	return req, nil
}

// splitScopes reads a scope answer: space-separated (RFC 6749), or
// comma-separated as GitHub sends them.
func splitScopes(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' })
}

func hostOf(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil {
		return strings.ToLower(u.Hostname())
	}
	return ""
}
