package keychain

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/scheme"
)

type openAITransport func(*http.Request) (*http.Response, error)

func (f openAITransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// planProvider is a synthetic OpenAI issuer and Responses endpoint. Its
// transport refuses every other address, so these tests cannot spend quota.
type planProvider struct {
	t                                       *testing.T
	mu                                      sync.Mutex
	key                                     *rsa.PrivateKey
	ec                                      *ecdsa.PrivateKey
	mode                                    string
	nonce, challenge, redirect, scope, host string
	exchanges, refreshes, revokes, turns    int
	replayed                                chan error // "replay": the second callback's outcome
}

func newPlanProvider(t *testing.T) *planProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &planProvider{t: t, key: key, ec: ec}
}

func (p *planProvider) browser(raw string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	q := u.Query()
	if u.Scheme != "https" || u.Host != "auth.openai.com" || u.Path != "/api/accounts/authorize" || q.Get("resource") != "https://api.openai.com/v1" || q.Get("nonce") == "" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("ext_agent_host_id") == "" {
		return fmt.Errorf("invalid authorization parameters")
	}
	if q.Get("client_id") == "dynamic_agent_client" {
		if q.Get("agent_name_hint") != "nodwyn" {
			return fmt.Errorf("no app name")
		}
	} else if q.Get("client_id") != "oaiapp_fake" || q.Get("agent_name_hint") != "" {
		return fmt.Errorf("incorrect returning client")
	}
	if p.host != "" && p.host != q.Get("ext_agent_host_id") {
		return fmt.Errorf("host changed")
	}
	p.host = q.Get("ext_agent_host_id")
	p.nonce, p.challenge, p.redirect, p.scope = q.Get("nonce"), q.Get("code_challenge"), q.Get("redirect_uri"), q.Get("scope")
	cb, err := url.Parse(p.redirect)
	if err != nil || cb.Hostname() != "127.0.0.1" || cb.Path != "/auth/callback" {
		return fmt.Errorf("incorrect callback")
	}
	answer := url.Values{"state": {q.Get("state")}, "code": {"fake-code"}}
	if q.Get("client_id") == "dynamic_agent_client" {
		answer.Set("client_id", "oaiapp_fake")
	}
	switch p.mode {
	case "state":
		answer.Set("state", "wrong")
	case "denied":
		answer.Set("error", "access_denied")
	case "missing-client":
		answer.Del("client_id")
	case "dynamic-client":
		answer.Set("client_id", "dynamic_agent_client")
	case "changed-client":
		answer.Set("client_id", "oaiapp_other")
	}
	cb.RawQuery = answer.Encode()
	resp, err := http.Get(cb.String())
	if err != nil {
		return err
	}
	resp.Body.Close()
	if p.mode == "replay" {
		// The callback is hit again, as a reloaded tab would, after the
		// exchange it started has begun. Only a sleep orders the two: any lock
		// or channel between them would also order the handler's read after
		// the exchange's write, and the race detector would see nothing.
		p.replayed = make(chan error, 1)
		go func() {
			time.Sleep(100 * time.Millisecond)
			resp, err := http.Get(cb.String())
			if err == nil {
				resp.Body.Close()
			}
			p.replayed <- err
		}()
	}
	return nil
}

func (p *planProvider) client() *http.Client {
	return &http.Client{Transport: openAITransport(p.request), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (p *planProvider) request(r *http.Request) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	body := ""
	contentType := "application/json"
	encode := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			p.t.Fatal(err)
		}
		return string(b)
	}
	if r.URL.Scheme != "https" {
		return nil, fmt.Errorf("fake refuses non-https")
	}
	switch r.URL.Host + r.URL.Path {
	case "auth.openai.com/.well-known/openid-configuration":
		jwks, revoke := "https://auth.openai.com/.well-known/jwks.json", "https://auth.openai.com/api/accounts/oauth/revoke"
		if p.mode == "unsafe-jwks" {
			jwks = "https://stranger.example/keys"
		}
		if p.mode == "unsafe-revoke" {
			revoke = "https://stranger.example/revoke"
		}
		body = encode(map[string]string{"issuer": "https://auth.openai.com", "jwks_uri": jwks, "revocation_endpoint": revoke})
	case "auth.openai.com/.well-known/jwks.json":
		if p.mode == "es256" {
			pub, err := p.ec.PublicKey.Bytes()
			if err != nil {
				return nil, err
			}
			body = encode(map[string]any{"keys": []any{map[string]string{"kty": "EC", "kid": "fake-key", "alg": "ES256", "use": "sig", "crv": "P-256", "x": base64.RawURLEncoding.EncodeToString(pub[1:33]), "y": base64.RawURLEncoding.EncodeToString(pub[33:])}}})
			break
		}
		body = encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "fake-key", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.key.E)).Bytes())}}})
	case "auth.openai.com/api/accounts/oauth/token":
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if r.Method != "POST" || r.Form.Get("client_id") != "oaiapp_fake" || r.Form.Get("client_secret") != "" || r.Form.Get("resource") != "https://api.openai.com/v1" {
			return nil, fmt.Errorf("incorrect token parameters")
		}
		refreshing := r.Form.Get("grant_type") == "refresh_token"
		if refreshing {
			// A refresh that names no new refresh token leaves the first one in use.
			held := p.refreshes
			if p.mode == "refresh-omits" {
				held = 0
			}
			if r.Form.Get("refresh_token") != fmt.Sprintf("fake-plan-refresh-%d", held) {
				return nil, fmt.Errorf("refresh did not use latest token")
			}
			p.refreshes++
		} else {
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "fake-code" || r.Form.Get("redirect_uri") != p.redirect || base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge {
				return nil, fmt.Errorf("incorrect PKCE exchange")
			}
			p.exchanges++
			if p.mode == "replay" {
				time.Sleep(300 * time.Millisecond) // the repeated callback arrives meanwhile
			}
		}
		now := time.Now().Unix()
		claims := map[string]any{"iss": "https://auth.openai.com", "sub": "fake-account", "aud": "oaiapp_fake", "exp": now + 3600, "iat": now, "nonce": p.nonce}
		switch p.mode {
		case "nonce":
			claims["nonce"] = "wrong"
		case "issuer":
			claims["iss"] = "https://stranger.example"
		case "audience":
			claims["aud"] = "other-client"
		case "expired":
			claims["exp"] = now - 1
		case "subject":
			claims["sub"] = "other-account"
		case "no-subject":
			claims["sub"] = ""
		}
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"fake-key"}`))
		if p.mode == "es256" {
			header = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","kid":"fake-key"}`))
		}
		unsigned := header + "." + base64.RawURLEncoding.EncodeToString([]byte(encode(claims)))
		hash := sha256.Sum256([]byte(unsigned))
		sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, hash[:])
		if err != nil {
			return nil, err
		}
		if p.mode == "es256" {
			r, s, err := ecdsa.Sign(rand.Reader, p.ec, hash[:])
			if err != nil {
				return nil, err
			}
			sig = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
		}
		if p.mode == "signature" {
			sig[0] ^= 1
		}
		tok := map[string]any{"access_token": fmt.Sprintf("fake-plan-access-%d", p.refreshes), "refresh_token": fmt.Sprintf("fake-plan-refresh-%d", p.refreshes), "id_token": unsigned + "." + base64.RawURLEncoding.EncodeToString(sig), "token_type": "Bearer", "expires_in": 3600, "scope": p.scope}
		switch p.mode {
		case "scope":
			tok["scope"] = "openid profile email offline_access"
		case "no-id-token":
			delete(tok, "id_token")
		case "token-type":
			tok["token_type"] = "Basic"
		case "no-refresh":
			delete(tok, "refresh_token")
		case "no-expiry":
			tok["expires_in"] = 0
		case "refresh-omits":
			if refreshing {
				delete(tok, "refresh_token")
				delete(tok, "scope")
			}
		case "refresh-narrows":
			if refreshing {
				tok["scope"] = "openid profile email offline_access"
			}
		}
		body = encode(tok)
	case "auth.openai.com/api/accounts/oauth/revoke":
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if r.Form.Get("client_id") != "oaiapp_fake" || r.Form.Get("token") != fmt.Sprintf("fake-plan-refresh-%d", p.refreshes) || r.Form.Get("token_type_hint") != "refresh_token" {
			return nil, fmt.Errorf("incorrect revocation")
		}
		p.revokes++
	case "api.openai.com/v1/models":
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer fake-plan-access-%d", p.refreshes) {
			return nil, fmt.Errorf("incorrect models token")
		}
		body = `{"models":[{"slug":"plan-model","visibility":"list"},{"slug":"hidden-model","visibility":"hide"}]}`
	case "api.openai.com/v1/responses":
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer fake-plan-access-%d", p.refreshes) {
			return nil, fmt.Errorf("incorrect inference token")
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, err
		}
		if req["stream"] != true || req["store"] != false || req["model"] != "plan-model" {
			return nil, fmt.Errorf("incorrect Responses request")
		}
		p.turns++
		contentType = "text/event-stream"
		body = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	default:
		return nil, fmt.Errorf("fake refuses address %s", r.URL.Host+r.URL.Path)
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func planGrant(t *testing.T) Grant {
	t.Helper()
	p, ok := ProfileNamed("openai")
	if !ok {
		t.Fatal("OpenAI OAuth profile is missing")
	}
	return Grant{Profile: p}
}

func TestOpenAIPlanRegistrationRefreshInferenceAndRevoke(t *testing.T) {
	st := newStore(t, []byte("fake-pass"))
	p := newPlanProvider(t)
	st.SetHTTPClient(p.client())
	h, err := st.AddOAuth2(context.Background(), "chatgpt", planGrant(t), p.browser)
	if err != nil {
		t.Fatal(err)
	}
	info, err := st.Info(h.ID)
	if err != nil || info.Provider != "openai" || !info.Refreshable {
		t.Fatalf("info: %+v %v", info, err)
	}
	if err := st.Reauthorize(context.Background(), h.ID, p.browser); err != nil {
		t.Fatal(err)
	}
	if p.exchanges != 2 {
		t.Fatalf("exchanges: %d", p.exchanges)
	}
	st, err = Open(st.path, []byte("fake-pass"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetHTTPClient(p.client())
	if err := st.Reauthorize(context.Background(), h.ID, p.browser); err != nil {
		t.Fatal(err)
	}
	st.SetClock(func() time.Time { return time.Now().Add(3590 * time.Second) })
	model := llm.NewOpenAI("https://api.openai.com/v1", func(r *http.Request) error {
		if err := st.Refresh(r.Context(), h.ID, nil); err != nil {
			return err
		}
		return st.InjectHeader(h.ID, "Authorization", "Bearer ", r)
	})
	model.Client, model.Dialect = p.client(), llm.ChatResponses
	models, err := model.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "plan-model" {
		t.Fatalf("models: %+v %v", models, err)
	}
	out, err := model.Chat(context.Background(), llm.Request{Model: models[0].ID, Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}}, nil)
	if err != nil || out.Message.Content != "ok" || out.Usage.OutputTokens != 1 || p.turns != 1 || p.refreshes != 1 {
		t.Fatalf("inference: %+v %v refresh=%d turns=%d", out, err, p.refreshes, p.turns)
	}
	revoked, err := st.Revoke(context.Background(), h.ID)
	if err != nil || !revoked || p.revokes != 1 || st.Count() != 0 {
		t.Fatalf("revoke: %v %v count=%d", revoked, err, st.Count())
	}
	raw, err := os.ReadFile(st.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fake-plan-") || strings.Contains(fmt.Sprintf("%+v %+v", h, info), "fake-plan-") {
		t.Fatal("token escaped keychain")
	}
}

func TestOpenAIPlanRefusesInvalidAuthorization(t *testing.T) {
	for _, mode := range []string{"state", "denied", "missing-client", "dynamic-client", "nonce", "issuer", "audience", "expired", "no-subject", "signature", "scope", "no-id-token", "token-type", "no-refresh", "no-expiry", "unsafe-jwks", "unsafe-revoke"} {
		t.Run(mode, func(t *testing.T) {
			st := newStore(t, nil)
			p := newPlanProvider(t)
			p.mode = mode
			st.SetHTTPClient(p.client())
			_, err := st.AddOAuth2(context.Background(), "chatgpt", planGrant(t), p.browser)
			if err == nil || st.Count() != 0 {
				t.Fatalf("invalid authorization stored: %v", err)
			}
			if strings.Contains(err.Error(), "fake-plan-") {
				t.Fatal("token in diagnostic")
			}
		})
	}
}

// A refresh response may leave out the refresh token and the scopes (RFC 6749
// section 6): the credential keeps working on what the sign-in verified. One
// that names scopes without the plan's is refused, and the credential then
// asks for a new sign-in.
func TestOpenAIPlanRefreshChecksOnlyWhatItNames(t *testing.T) {
	signIn := func(t *testing.T, mode string) (*Store, *planProvider, *scheme.Handle, *time.Time) {
		t.Helper()
		st := newStore(t, nil)
		p := newPlanProvider(t)
		st.SetHTTPClient(p.client())
		h, err := st.AddOAuth2(context.Background(), "chatgpt", planGrant(t), p.browser)
		if err != nil {
			t.Fatal(err)
		}
		p.mode = mode
		now := time.Now()
		st.SetClock(func() time.Time { return now })
		return st, p, h, &now
	}
	t.Run("omits", func(t *testing.T) {
		st, p, h, now := signIn(t, "refresh-omits")
		for i := 1; i <= 2; i++ {
			*now = now.Add(2 * time.Hour)
			if err := st.Refresh(context.Background(), h.ID, nil); err != nil {
				t.Fatalf("refresh %d: %v", i, err)
			}
			info, err := st.Info(h.ID)
			if err != nil || info.NeedsReauth || !info.Refreshable || len(info.Scopes) == 0 || !strings.Contains(strings.Join(info.Scopes, " "), "chatgpt.tokens.use.direct") {
				t.Fatalf("refresh %d changed the credential: %+v %v", i, info, err)
			}
			r, err := http.NewRequest("POST", "https://api.openai.com/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.InjectHeader(h.ID, "Authorization", "Bearer ", r); err != nil || r.Header.Get("Authorization") != fmt.Sprintf("Bearer fake-plan-access-%d", i) || p.refreshes != i {
				t.Fatalf("refresh %d: header %q refreshes %d: %v", i, r.Header.Get("Authorization"), p.refreshes, err)
			}
		}
	})
	t.Run("narrows", func(t *testing.T) {
		st, _, h, now := signIn(t, "refresh-narrows")
		*now = now.Add(2 * time.Hour)
		if err := st.Refresh(context.Background(), h.ID, nil); err == nil {
			t.Fatal("a refresh without the plan scopes was accepted")
		}
		if info, err := st.Info(h.ID); err != nil || !info.NeedsReauth {
			t.Fatalf("the credential was not marked for a new sign-in: %+v %v", info, err)
		}
	})
}

// A second request to the callback, while the exchange of the first runs, must
// not read what that exchange writes. Only the race detector fails this on the
// old code, so it needs go test -race, as make accept runs it.
func TestOpenAIPlanCallbackRepeatedDuringExchange(t *testing.T) {
	st := newStore(t, nil)
	p := newPlanProvider(t)
	p.mode = "replay"
	st.SetHTTPClient(p.client())
	h, err := st.AddOAuth2(context.Background(), "chatgpt", planGrant(t), p.browser)
	if err != nil || st.Count() != 1 {
		t.Fatalf("sign-in with a repeated callback: %v count=%d", err, st.Count())
	}
	if err := <-p.replayed; err != nil {
		t.Fatalf("the repeated callback was not answered: %v", err)
	}
	if info, err := st.Info(h.ID); err != nil || !info.Refreshable {
		t.Fatalf("info: %+v %v", info, err)
	}
}

func TestOpenAIPlanFailedReauthorizationKeepsCredential(t *testing.T) {
	for _, mode := range []string{"subject", "changed-client", "scope", "signature"} {
		t.Run(mode, func(t *testing.T) {
			st := newStore(t, nil)
			p := newPlanProvider(t)
			st.SetHTTPClient(p.client())
			h, err := st.AddOAuth2(context.Background(), "chatgpt", planGrant(t), p.browser)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(st.path)
			if err != nil {
				t.Fatal(err)
			}
			p.mode = mode
			if err := st.Reauthorize(context.Background(), h.ID, p.browser); err == nil {
				t.Fatal("invalid reauthorization accepted")
			}
			after, err := os.ReadFile(st.path)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("failed reauthorization replaced the credential")
			}
		})
	}
}

func TestOpenAIPlanTokenCannotReachOtherSinks(t *testing.T) {
	st := newStore(t, nil)
	p := newPlanProvider(t)
	st.SetHTTPClient(p.client())
	h, err := st.AddOAuth2(context.Background(), "chatgpt", planGrant(t), p.browser)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"https://stranger.example/v1/responses", "https://api.openai.com/v1/chat/completions", "http://api.openai.com/v1/responses", "https://api.openai.com:8443/v1/responses", "https://api.openai.com/v1/responses?token=1"} {
		r, err := http.NewRequest("POST", raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.InjectHeader(h.ID, "Authorization", "Bearer ", r); err == nil || r.Header.Get("Authorization") != "" {
			t.Fatalf("token sent to %s", raw)
		}
	}
	r, err := http.NewRequest("POST", "https://api.openai.com/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.InjectQuery(h.ID, "token", r) == nil || st.InjectBody(h.ID, r) == nil || st.InjectEnv(h.ID, "TOKEN", exec.Command("unused")) == nil || st.InjectHeader(h.ID, "X-Token", "", r) == nil {
		t.Fatal("token left through another sink")
	}
}

func TestOpenAIPlanCancelledRegistrationReusesHost(t *testing.T) {
	st := newStore(t, nil)
	p := newPlanProvider(t)
	p.mode = "denied"
	st.SetHTTPClient(p.client())
	if _, err := st.AddOAuth2(context.Background(), "chatgpt", planGrant(t), p.browser); err == nil {
		t.Fatal("consent denial accepted")
	}
	st, err := Open(st.path, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.mode = ""
	st.SetHTTPClient(p.client())
	if _, err := st.AddOAuth2(context.Background(), "chatgpt", planGrant(t), p.browser); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAIPlanAcceptsES256Identity(t *testing.T) {
	st := newStore(t, nil)
	p := newPlanProvider(t)
	p.mode = "es256"
	st.SetHTTPClient(p.client())
	if _, err := st.AddOAuth2(context.Background(), "chatgpt", planGrant(t), p.browser); err != nil {
		t.Fatal(err)
	}
}
