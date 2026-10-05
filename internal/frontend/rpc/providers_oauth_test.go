package rpc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/fclaude/nodwyn/internal/config"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/keychain"
	fakeoauth "github.com/fclaude/nodwyn/test/fakes/oauth"
)

func TestModelProbeRefreshesOAuthOnlyAfterBindingAllowsIt(t *testing.T) {
	st, err := keychain.Create(t.TempDir()+"/keychain", keychain.FastKDF, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := fakeoauth.New("probe-test")
	defer p.Close()
	p.SetExpiresIn(60)
	h, err := st.AddOAuth2(context.Background(), "model-plan", keychain.Grant{Profile: keychain.Custom(p.AuthURL(), p.TokenURL(), p.RevokeURL()), ClientID: p.ClientID, Scopes: []string{"read", "write"}}, p.Browser)
	if err != nil {
		t.Fatal(err)
	}
	st.SetClock(func() time.Time { return time.Now().Add(2 * time.Minute) })
	row, err := binding.Exact("model-plan", "api.example.com", "POST", config.KeyPositionBearer)
	if err != nil {
		t.Fatal(err)
	}
	row.Scopes = []string{"read"}
	r, err := http.NewRequest("POST", "https://api.example.com/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := authorizer(st, binding.Table{Bind: []binding.Binding{row}}, "model-plan", config.KeyPositionBearer)(r); err == nil || p.Refreshes() != 0 || r.Header.Get("Authorization") != "" {
		t.Fatalf("outside scope refreshed or injected: %v", err)
	}
	row.Scopes = []string{"read", "write"}
	if err := authorizer(st, binding.Table{Bind: []binding.Binding{row}}, "model-plan", config.KeyPositionBearer)(r); err != nil {
		t.Fatal(err)
	}
	if p.Refreshes() != 1 || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer fake-access-token-") {
		t.Fatal("model probe did not refresh and inject")
	}
	if err := st.Delete(h.ID); err != nil {
		t.Fatal(err)
	}
}

// planCredential supplies a signed identity without reaching OpenAI. The
// OAuth fake checks PKCE, while this transport supplies discovery and JWKS.
func planCredential(t *testing.T, st *keychain.Store, class string) string {
	t.Helper()
	issuer := fakeoauth.New("oaiapp_fake")
	defer issuer.Close()
	signing, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var nonce string
	encode := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	st.SetHTTPClient(&http.Client{Transport: planRPCTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.String() {
		case "https://auth.openai.com/.well-known/openid-configuration":
			body = `{"issuer":"https://auth.openai.com","jwks_uri":"https://auth.openai.com/keys"}`
		case "https://auth.openai.com/keys":
			body = encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "fake", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(signing.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(signing.E)).Bytes())}}})
		default:
			if r.URL.String() != issuer.TokenURL() {
				return nil, fmt.Errorf("fake refuses %s", r.URL)
			}
			resp, err := http.DefaultTransport.RoundTrip(r)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			var tokens map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
				return nil, err
			}
			now := time.Now().Unix()
			claims := encode(map[string]any{"iss": "https://auth.openai.com", "sub": "fake-account", "aud": "oaiapp_fake", "nonce": nonce, "exp": now + 3600, "iat": now})
			unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"fake"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(claims))
			hash := sha256.Sum256([]byte(unsigned))
			sig, err := rsa.SignPKCS1v15(rand.Reader, signing, crypto.SHA256, hash[:])
			if err != nil {
				return nil, err
			}
			tokens["id_token"] = unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
			body = encode(tokens)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})})
	profile := keychain.Custom(issuer.AuthURL(), issuer.TokenURL(), "")
	profile.Name = "openai"
	h, err := st.AddOAuth2(context.Background(), class, keychain.Grant{Profile: profile, ClientID: issuer.ClientID, Scopes: []string{"openid", "resource.invoke", "chatgpt.tokens.use.direct"}}, func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		nonce = u.Query().Get("nonce")
		return issuer.Browser(raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	return h.ID
}

type planRPCTransport func(*http.Request) (*http.Response, error)

func (f planRPCTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderSelectsChatGPTCredentialAndPreservesItWhenSwitchingToKey(t *testing.T) {
	o := options(t)
	if err := os.MkdirAll(o.Paths.ConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.Paths.ConfigFile, []byte("# Keep this comment.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := keychain.Create(o.Paths.Keychain, keychain.FastKDF, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := planCredential(t, st, "chatgpt")
	o.Keychain = st
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	var settings settingsResult
	into(t, c.ok("settings.provider.write", map[string]any{"provider": "openai", "url": "https://api.openai.com/v1", "dialect": "responses", "credential": id}), &settings)
	p := settings.Servers[0]
	if p.Credential != "chatgpt" || p.Key != "ready" {
		t.Fatalf("provider: %+v", p)
	}
	var wire map[string]any
	if err := json.Unmarshal(c.ok("settings.read", map[string]any{}), &wire); err != nil {
		t.Fatal(err)
	}
	servers := wire["servers"].([]any)
	if servers[0].(map[string]any)["authentication"] != "chatgpt" {
		t.Fatalf("authentication not reported: %v", servers)
	}
	cfg, err := config.Load(o.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Servers[p.Name].Credential != "chatgpt" || !strings.Contains(read(t, o.Paths.ConfigFile), "# Keep this comment.") {
		t.Fatal("provider not persisted")
	}
	table, err := binding.Load(o.Paths.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	info, err := st.Info(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		if bound, _ := table.Lookup("chatgpt", "api.openai.com", method, config.KeyPositionBearer, info.Scopes...); !bound {
			t.Fatal("provider save omitted model binding")
		}
		if bound, _ := table.Lookup("chatgpt", "stranger.example", method, config.KeyPositionBearer, info.Scopes...); bound {
			t.Fatal("binding escaped provider host")
		}
	}
	static, err := st.Add("another-key", []byte("fake-static-key"))
	if err != nil {
		t.Fatal(err)
	}
	before := read(t, o.Paths.ConfigFile)
	for _, bad := range []map[string]any{
		{"name": p.Name, "provider": "openai", "url": p.URL, "dialect": "responses", "credential": static.ID},
		{"name": p.Name, "provider": "openai", "url": p.URL, "dialect": "responses", "credential": "missing-id"},
		{"name": p.Name, "provider": "openai", "url": "https://api.openai.com/v1/other", "dialect": "responses"},
		{"provider": "openai", "url": "https://stranger.example/v1", "dialect": "responses", "credential": id},
		{"name": p.Name, "provider": "openai", "url": "https://api.openai.com/v1", "credential": id},
		{"name": p.Name, "provider": "openai", "url": "https://api.openai.com/v1", "dialect": "responses", "credential": id, "api_key": "fake-key"},
	} {
		if c.call("settings.provider.write", bad).Error == nil {
			t.Fatal("invalid plan provider accepted")
		}
	}
	if after := read(t, o.Paths.ConfigFile); after != before {
		t.Fatal("refused provider write changed config")
	}
	into(t, c.ok("settings.provider.write", map[string]any{"name": p.Name, "provider": "openai", "url": p.URL, "dialect": "responses", "api_key": "fake-static-model-key"}), &settings)
	if settings.Servers[0].Credential == "chatgpt" {
		t.Fatal("API key did not switch authentication")
	}
	info, err = st.Info(id)
	if err != nil || info.Kind != keychain.KindOAuth2 {
		t.Fatal("switching destroyed the connected account")
	}
	// The plan comes back as a second server at the same address, beside the
	// API-key one, and neither disturbs the other.
	into(t, c.ok("settings.provider.write", map[string]any{"name": "chatgpt-plan", "provider": "openai", "url": p.URL, "dialect": "responses", "credential": id}), &settings)
	if len(settings.Servers) != 2 {
		t.Fatalf("servers: %+v", settings.Servers)
	}
	byName := map[string]modelServer{}
	for _, sv := range settings.Servers {
		byName[sv.Name] = sv
	}
	if byName[p.Name].Credential == "chatgpt" || byName[p.Name].Authentication != "" || byName["chatgpt-plan"].Credential != "chatgpt" || byName["chatgpt-plan"].Authentication != "chatgpt" {
		t.Fatalf("the two servers share a credential: %+v", byName)
	}
}
