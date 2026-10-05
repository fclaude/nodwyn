package keychain

// OpenAI plan authentication extends ADR 24's PKCE flow with dynamic client
// registration and a verified OIDC identity. Endpoints are fixed here, rather
// than taken from the callback or from a file a project can write.

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

const openAIIssuer = "https://auth.openai.com"
const openAIResource = "https://api.openai.com/v1"

type openAIDiscovery struct {
	Issuer             string `json:"issuer"`
	JWKSURI            string `json:"jwks_uri"`
	RevocationEndpoint string `json:"revocation_endpoint"`
}

// openAIHost saves a stable, opaque host identifier before the browser opens,
// including when consent is cancelled. The keychain lock serializes callers.
func (st *Store) openAIHost() (string, error) {
	var host string
	err := st.change(func() error {
		if st.openAIHostID == "" {
			b := random(16)
			b[6] = (b[6] & 15) | 64
			b[8] = (b[8] & 63) | 128
			st.openAIHostID = fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
		}
		host = st.openAIHostID
		return nil
	})
	return host, err
}

// openAIJSON reads public authentication metadata with the same bounded,
// non-redirecting client as token exchange. No token is sent with it.
func (st *Store) openAIJSON(ctx context.Context, endpoint string, out any) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host != "auth.openai.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("keychain: OpenAI authentication metadata must be on https://auth.openai.com")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := st.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("keychain: OpenAI authentication metadata: %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return errors.New("keychain: invalid OpenAI authentication metadata")
	}
	return nil
}

func (st *Store) openAIDiscover(ctx context.Context) (openAIDiscovery, error) {
	var d openAIDiscovery
	if err := st.openAIJSON(ctx, openAIIssuer+"/.well-known/openid-configuration", &d); err != nil {
		return d, err
	}
	if d.Issuer != openAIIssuer || d.JWKSURI == "" {
		return d, errors.New("keychain: OpenAI discovery has the wrong issuer or no signing keys")
	}
	if d.RevocationEndpoint != "" {
		u, err := url.Parse(d.RevocationEndpoint)
		if err != nil || u.Scheme != "https" || u.Host != "auth.openai.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return d, errors.New("keychain: OpenAI discovery has an unsafe revocation endpoint")
		}
	}
	return d, nil
}

// checkOpenAIGrant checks a token response for plan usage. A sign-in must
// name a refresh token and both plan scopes. A refresh may leave either out,
// as RFC 6749 section 6 lets it, and then the stored refresh token and the
// scopes the sign-in verified stay: refusing that would mark a working
// credential for sign-in again over a field the provider need not send. A
// refresh that does name scopes must still name both.
func checkOpenAIGrant(tok token, refreshing bool) error {
	if !strings.EqualFold(tok.TokenType, "Bearer") || tok.ExpiresIn <= 0 || (tok.RefreshToken == "" && !refreshing) {
		return errors.New("keychain: OpenAI returned no renewable bearer grant")
	}
	granted := splitScopes(tok.Scope)
	if refreshing && len(granted) == 0 {
		return nil
	}
	for _, scope := range []string{"resource.invoke", "chatgpt.tokens.use.direct"} {
		if !slices.Contains(granted, scope) {
			return errors.New("keychain: OpenAI did not grant ChatGPT plan usage; authorize again with plan usage enabled")
		}
	}
	return nil
}

// openAIIdentity verifies only asymmetric JWT algorithms and keys from the
// fixed issuer's JWKS, fetched anew for each sign-in so rotation is honoured.
// Token text is never included in a diagnostic.
func (st *Store) openAIIdentity(ctx context.Context, jwks, raw, clientID, nonce string) (string, error) {
	invalid := errors.New("keychain: OpenAI ID token failed signature or identity validation")
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", invalid
	}
	decode := func(part string, out any) bool {
		b, err := base64.RawURLEncoding.DecodeString(part)
		return err == nil && json.Unmarshal(b, out) == nil
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	var claims struct {
		Issuer          string          `json:"iss"`
		Subject         string          `json:"sub"`
		Audience        json.RawMessage `json:"aud"`
		Nonce           string          `json:"nonce"`
		Expiry          int64           `json:"exp"`
		Issued          int64           `json:"iat"`
		NotBefore       int64           `json:"nbf"`
		AuthorizedParty string          `json:"azp"`
	}
	if !decode(parts[0], &header) || !decode(parts[1], &claims) || header.Kid == "" || (header.Alg != "RS256" && header.Alg != "ES256") {
		return "", invalid
	}
	var audience string
	var audiences []string
	if json.Unmarshal(claims.Audience, &audience) == nil {
		audiences = []string{audience}
	} else if json.Unmarshal(claims.Audience, &audiences) != nil {
		return "", invalid
	}
	now := st.now().Unix()
	if claims.Issuer != openAIIssuer || claims.Subject == "" || !slices.Contains(audiences, clientID) || claims.Nonce != nonce || claims.Expiry <= now || claims.Issued <= 0 || claims.Issued > now+5 || claims.NotBefore > now+5 || (len(audiences) > 1 && claims.AuthorizedParty != clientID) || (claims.AuthorizedParty != "" && claims.AuthorizedParty != clientID) {
		return "", invalid
	}
	var keys struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := st.openAIJSON(ctx, jwks, &keys); err != nil {
		return "", err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", invalid
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	number := func(s string) *big.Int {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			return new(big.Int)
		}
		return new(big.Int).SetBytes(b)
	}
	for _, key := range keys.Keys {
		if key.Kid != header.Kid || (key.Alg != "" && key.Alg != header.Alg) || (key.Use != "" && key.Use != "sig") {
			continue
		}
		switch {
		case header.Alg == "RS256" && key.Kty == "RSA":
			n, e := number(key.N), number(key.E)
			if n.BitLen() < 2048 || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
				continue
			}
			if rsa.VerifyPKCS1v15(&rsa.PublicKey{N: n, E: int(e.Int64())}, crypto.SHA256, hash[:], sig) == nil {
				return claims.Subject, nil
			}
		case header.Alg == "ES256" && key.Kty == "EC" && key.Crv == "P-256" && len(sig) == 64:
			x, xerr := base64.RawURLEncoding.DecodeString(key.X)
			y, yerr := base64.RawURLEncoding.DecodeString(key.Y)
			if xerr != nil || yerr != nil || len(x) != 32 || len(y) != 32 {
				continue
			}
			pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
			if err == nil && ecdsa.Verify(pub, hash[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
				return claims.Subject, nil
			}
		}
	}
	return "", invalid
}

// checkOpenAISink keeps a plan grant on the two public endpoints it grants,
// even when a user binds its class more broadly. Other credentials continue
// to follow their binding rows alone.
func (st *Store) checkOpenAISink(id, name, prefix string, req *http.Request) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.freshLocked()
	e := st.entries[id]
	if e.oauth == nil || e.oauth.Provider != "openai" {
		return nil
	}
	if req != nil && req.URL != nil && req.URL.Scheme == "https" && req.URL.Host == "api.openai.com" && req.URL.User == nil && req.URL.RawQuery == "" && req.URL.Fragment == "" && strings.EqualFold(name, "Authorization") && prefix == "Bearer " && ((req.Method == http.MethodGet && req.URL.Path == "/v1/models") || (req.Method == http.MethodPost && req.URL.Path == "/v1/responses")) {
		return nil
	}
	return errors.New("keychain: a ChatGPT plan credential is only sent as a bearer header to https://api.openai.com/v1/models (GET) or /v1/responses (POST); configure dialect = \"responses\"")
}
