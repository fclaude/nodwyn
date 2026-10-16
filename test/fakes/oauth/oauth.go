// Package oauth is a fake OAuth2 provider for tests (ADR 24): an
// authorization endpoint that approves at once by redirecting back with a
// code, a token endpoint that checks PKCE and issues, refreshes and rotates
// tokens, and a revocation endpoint. Tokens are distinct strings, so a test
// can look for them where they must never be.
package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
)

// Server is the fake provider.
type Server struct {
	*httptest.Server
	ClientID string

	mu        sync.Mutex
	expiresIn int64 // access token lifetime in seconds; 0: tokens do not expire
	rotate    bool  // a new refresh token on every refresh, the old one retired
	deny      bool  // the authorization endpoint refuses (access_denied)
	n         int
	codes     map[string]pending
	refresh   map[string]string // refresh token -> scope
	revoked   []string
	refreshes int // refresh_token grants answered
	exchanges int // authorization_code grants answered
}

// Settings change how the fake answers from now on.
func (s *Server) SetExpiresIn(seconds int64) { s.mu.Lock(); s.expiresIn = seconds; s.mu.Unlock() }
func (s *Server) SetRotate(rotate bool)      { s.mu.Lock(); s.rotate = rotate; s.mu.Unlock() }
func (s *Server) SetDeny(deny bool)          { s.mu.Lock(); s.deny = deny; s.mu.Unlock() }

// Refreshes and Exchanges count the refresh_token and authorization_code
// grants answered.
func (s *Server) Refreshes() int { s.mu.Lock(); defer s.mu.Unlock(); return s.refreshes }
func (s *Server) Exchanges() int { s.mu.Lock(); defer s.mu.Unlock(); return s.exchanges }

type pending struct {
	challenge, redirect, scope string
}

// New starts a fake provider for clientID.
func New(clientID string) *Server {
	s := &Server{ClientID: clientID, expiresIn: 3600, codes: map[string]pending{}, refresh: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", s.authorize)
	mux.HandleFunc("/token", s.token)
	mux.HandleFunc("/revoke", s.revoke)
	s.Server = httptest.NewServer(mux)
	return s
}

// AuthURL, TokenURL and RevokeURL are the provider's endpoints.
func (s *Server) AuthURL() string   { return s.URL + "/authorize" }
func (s *Server) TokenURL() string  { return s.URL + "/token" }
func (s *Server) RevokeURL() string { return s.URL + "/revoke" }

// Browser stands in for the user's browser: it follows the authorization
// URL, and the fake's redirect, to the loopback callback.
func (s *Server) Browser(authURL string) error {
	go func() {
		resp, err := http.Get(authURL)
		if err == nil {
			resp.Body.Close()
		}
	}()
	return nil
}

// RevokeAll retires every refresh token, as a user revoking the app does.
func (s *Server) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for rt := range s.refresh {
		s.revoked = append(s.revoked, rt)
	}
	s.refresh = map[string]string{}
}

// Revoked lists the tokens revoked, through the endpoint or RevokeAll.
func (s *Server) Revoked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.revoked...)
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || q.Get("client_id") != s.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	back := url.Values{"state": {q.Get("state")}}
	if s.deny {
		back.Set("error", "access_denied")
	} else {
		s.n++
		code := fmt.Sprintf("code-%d", s.n)
		s.codes[code] = pending{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), scope: q.Get("scope")}
		back.Set("code", code)
	}
	s.mu.Unlock()
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.Method != http.MethodPost {
		http.Error(w, "bad token request", http.StatusBadRequest)
		return
	}
	fail := func(code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	if r.PostForm.Get("client_id") != s.ClientID {
		fail("invalid_client")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var scope, refresh string
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		p, ok := s.codes[r.PostForm.Get("code")]
		delete(s.codes, r.PostForm.Get("code"))
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge || r.PostForm.Get("redirect_uri") != p.redirect {
			fail("invalid_grant")
			return
		}
		s.exchanges++
		scope = p.scope
		s.n++
		refresh = fmt.Sprintf("fake-refresh-token-%d", s.n)
		s.refresh[refresh] = scope
	case "refresh_token":
		old := r.PostForm.Get("refresh_token")
		sc, ok := s.refresh[old]
		if !ok {
			fail("invalid_grant")
			return
		}
		s.refreshes++
		scope, refresh = sc, ""
		if s.rotate {
			delete(s.refresh, old)
			s.n++
			refresh = fmt.Sprintf("fake-refresh-token-%d", s.n)
			s.refresh[refresh] = sc
		}
	default:
		fail("unsupported_grant_type")
		return
	}
	s.n++
	answer := map[string]any{"access_token": fmt.Sprintf("fake-access-token-%d", s.n), "token_type": "Bearer", "scope": scope}
	if s.expiresIn > 0 {
		answer["expires_in"] = s.expiresIn
	}
	if refresh != "" {
		answer["refresh_token"] = refresh
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(answer)
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.Method != http.MethodPost || r.PostForm.Get("token") == "" {
		http.Error(w, "bad revocation request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tok := r.PostForm.Get("token")
	delete(s.refresh, tok)
	s.revoked = append(s.revoked, tok)
	w.WriteHeader(http.StatusOK)
}

// HasToken reports whether text contains any token the fake issued.
func HasToken(text string) bool {
	return strings.Contains(text, "fake-access-token-") || strings.Contains(text, "fake-refresh-token-")
}
