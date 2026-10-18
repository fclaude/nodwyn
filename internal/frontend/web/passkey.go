package web

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// Passkeys (WebAuthn) are checked with the standard library alone (ADR 30,
// amended 2026-10-17). The console sends what the browser reports: on
// registration the credential's public key as a SubjectPublicKeyInfo
// (getPublicKey()) beside the client data and the authenticator data, with
// attestation "none", so nothing needs CBOR; on an assertion the client data,
// the authenticator data and the signature. What is checked is what the
// WebAuthn relying party steps check when no attestation is asked for: in
// the client data the ceremony's type, the challenge this console issued and
// its origin, not from a frame; in the authenticator data the hash of the RP
// ID, the person's presence and their verification (a biometric or a PIN);
// and for an assertion the signature over the authenticator data and the
// client data's hash. A registration's key is taken as the page reports it:
// the page is this console's own, reached with an invitation, and
// attestation would prove only who made the authenticator.

// passkeyAlgorithms are the COSE algorithms the console asks a passkey for:
// ES256, EdDSA and RS256, which every authenticator offers one of.
var passkeyAlgorithms = []int{-7, -8, -257}

// Authenticator data flags (WebAuthn §6.1).
const (
	flagPresent  = 0x01
	flagVerified = 0x04
)

var errPasskey = errors.New("the passkey's answer does not check out; nothing was changed")

// unb64 decodes a field the console sent as unpadded base64url.
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// checkClientData checks the client data of a ceremony of type typ
// ("webauthn.create" or "webauthn.get") for challenge at origin.
func checkClientData(raw []byte, typ, challenge, origin string) error {
	var c struct {
		Type        string `json:"type"`
		Challenge   string `json:"challenge"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
	}
	if json.Unmarshal(raw, &c) != nil || c.Type != typ || c.Origin != origin || c.CrossOrigin ||
		subtle.ConstantTimeCompare([]byte(c.Challenge), []byte(challenge)) != 1 {
		return errPasskey
	}
	return nil
}

// checkAuthenticatorData checks that raw is for rpID and that the person was
// there and verified.
func checkAuthenticatorData(raw []byte, rpID string) error {
	want := sha256.Sum256([]byte(rpID))
	if len(raw) < 37 || !bytes.Equal(raw[:32], want[:]) || raw[32]&flagPresent == 0 || raw[32]&flagVerified == 0 {
		return errPasskey
	}
	return nil
}

// passkeyKey parses a passkey's public key, refusing what none of
// passkeyAlgorithms makes.
func passkeyKey(spki []byte) (crypto.PublicKey, error) {
	key, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, errPasskey
	}
	switch k := key.(type) {
	case *ecdsa.PublicKey:
		if k.Curve == elliptic.P256() {
			return k, nil
		}
	case ed25519.PublicKey:
		return k, nil
	case *rsa.PublicKey:
		if k.N.BitLen() >= 2048 {
			return k, nil
		}
	}
	return nil, errPasskey
}

// verifyAssertion checks an assertion's signature with key: it signs the
// authenticator data followed by the client data's SHA-256.
func verifyAssertion(key crypto.PublicKey, authenticatorData, clientData, signature []byte) error {
	hash := sha256.Sum256(clientData)
	signed := append(append([]byte{}, authenticatorData...), hash[:]...)
	digest := sha256.Sum256(signed)
	ok := false
	switch k := key.(type) {
	case *ecdsa.PublicKey:
		ok = ecdsa.VerifyASN1(k, digest[:], signature)
	case ed25519.PublicKey:
		ok = ed25519.Verify(k, signed, signature)
	case *rsa.PublicKey:
		ok = rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], signature) == nil
	}
	if !ok {
		return errPasskey
	}
	return nil
}

// ceremony is a passkey challenge the console issued and has not yet had
// answered: for a registration ("webauthn.create") or a sign-in or touch
// ("webauthn.get"), at the host the page was reached by.
type ceremony struct {
	kind    string
	host    string
	expires time.Time
}

// ceremonyLife bounds how long a challenge waits for its answer.
const ceremonyLife = 2 * time.Minute

// touchLife is how long a passkey touch lets a device change nodwyn's setup:
// each setup action renews it, so one sitting asks once (ADR 30, amended
// 2026-10-17).
const touchLife = 15 * time.Minute

// passkeyHost reports whether a page reached at host may use passkeys:
// browsers allow them only in a secure context, which is https or a
// localhost name, and only for a host name, never an address.
func passkeyHost(host string, secure bool) bool {
	name := hostName(host)
	if net.ParseIP(name) != nil {
		return false
	}
	return secure || name == "localhost" || strings.HasSuffix(name, ".localhost")
}

// hostName is host without its port: a passkey's RP ID.
func hostName(host string) string {
	if name, _, err := net.SplitHostPort(host); err == nil {
		return name
	}
	return host
}

// challenge issues a challenge for a ceremony of kind at host.
func (b *bridge) challenge(kind, host string) string {
	value := base64.RawURLEncoding.EncodeToString(random(32))
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ceremonies == nil {
		b.ceremonies = map[string]ceremony{}
	}
	now := time.Now()
	for c, x := range b.ceremonies {
		if now.After(x.expires) {
			delete(b.ceremonies, c)
		}
	}
	b.ceremonies[value] = ceremony{kind: kind, host: host, expires: now.Add(ceremonyLife)}
	return value
}

// answered spends a challenge: true once, for a live one of kind at host.
func (b *bridge) answered(value, kind, host string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	x, ok := b.ceremonies[value]
	delete(b.ceremonies, value)
	return ok && x.kind == kind && x.host == host && time.Now().Before(x.expires)
}

// touch starts or renews the touch of the device whose token has the digest
// device (Device.Hash): a device paired again under a revoked one's name
// inherits nothing.
func (b *bridge) touch(device string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.touches == nil {
		b.touches = map[string]time.Time{}
	}
	b.touches[device] = time.Now().Add(touchLife)
}

// touched reports whether the person of the device whose token has the
// digest device touched their passkey within touchLife of its last setup
// action.
func (b *bridge) touched(device string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Now().Before(b.touches[device])
}

func random(n int) []byte {
	raw := make([]byte, n)
	_, _ = rand.Read(raw) // crypto/rand does not fail
	return raw
}

// passkey answers the console's passkey requests, each a same-origin JSON
// POST:
//
//   - /passkey/options {invite?} issues a challenge: with an invitation that
//     may register a passkey, for a registration; without, for a sign-in or
//     a touch, with the registered passkeys' ids.
//   - /passkey/register {invite, device, challenge, id, client_data,
//     authenticator_data, public_key} registers the passkey and pairs the
//     device as the person's.
//   - /passkey/assert {device?, challenge, id, client_data,
//     authenticator_data, signature} is a touch from a paired device, which
//     also makes a device paired without a passkey the person's; from a
//     browser not paired, it signs in a new device named device.
func (b *bridge) passkey(w http.ResponseWriter, r *http.Request) {
	origin := b.scheme(r.Host) + "://" + r.Host
	if r.Method != http.MethodPost || r.Header.Get("Origin") != origin || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "Same-origin JSON POST required", http.StatusForbidden)
		return
	}
	if !b.shared() || b.readOnly || !passkeyHost(r.Host, b.secure(r.Host)) {
		http.Error(w, "Passkeys are used on a console other devices reach over https", http.StatusForbidden)
		return
	}
	var p struct {
		Invite            string `json:"invite"`
		Device            string `json:"device"`
		Challenge         string `json:"challenge"`
		ID                string `json:"id"`
		ClientData        string `json:"client_data"`
		AuthenticatorData string `json:"authenticator_data"`
		PublicKey         string `json:"public_key"`
		Signature         string `json:"signature"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		http.Error(w, "Invalid passkey request", http.StatusBadRequest)
		return
	}
	rpID := hostName(r.Host)
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Path {
	case "/passkey/options":
		if p.Invite != "" {
			if ok, passkey := b.devices.Invited(p.Invite); !ok {
				http.Error(w, errInvite.Error(), http.StatusForbidden)
				return
			} else if !passkey {
				http.Error(w, "This invitation pairs a device that answers, without a passkey", http.StatusForbidden)
				return
			}
			reply(map[string]any{"challenge": b.challenge("webauthn.create", r.Host), "rp_id": rpID,
				"user": base64.RawURLEncoding.EncodeToString(random(16)), "algorithms": passkeyAlgorithms})
			return
		}
		keys, err := b.devices.Passkeys()
		if err != nil || len(keys) == 0 {
			http.Error(w, "No passkey is registered yet: pair with an invitation", http.StatusForbidden)
			return
		}
		ids := []string{}
		for _, k := range keys {
			ids = append(ids, k.ID)
		}
		reply(map[string]any{"challenge": b.challenge("webauthn.get", r.Host), "rp_id": rpID, "credentials": ids})
	case "/passkey/register":
		clientData, err1 := unb64(p.ClientData)
		authData, err2 := unb64(p.AuthenticatorData)
		spki, err3 := unb64(p.PublicKey)
		_, err4 := unb64(p.ID)
		if errors.Join(err1, err2, err3, err4) != nil || p.ID == "" || !b.answered(p.Challenge, "webauthn.create", r.Host) {
			http.Error(w, errPasskey.Error(), http.StatusForbidden)
			return
		}
		if _, err := passkeyKey(spki); err != nil || checkClientData(clientData, "webauthn.create", p.Challenge, origin) != nil || checkAuthenticatorData(authData, rpID) != nil {
			http.Error(w, errPasskey.Error(), http.StatusForbidden)
			return
		}
		token, err := b.devices.Register(p.Invite, p.Device, Passkey{ID: p.ID, PublicKey: p.PublicKey})
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		b.signIn(w, r, token)
		b.touch(digest(token))
		b.logf("%s paired with an invitation and registered the passkey %s", p.Device, p.Device)
		reply(map[string]string{"device": p.Device})
	case "/passkey/assert":
		key, err := b.asserted(p.Challenge, p.ID, p.ClientData, p.AuthenticatorData, p.Signature, r.Host, origin)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		device, paired := b.authenticated(r)
		switch {
		case paired && device.Name == "":
			http.Error(w, "This computer needs no passkey", http.StatusForbidden)
		case paired:
			if err := b.devices.Touched(device.Name, key.Name); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			b.touch(device.Hash)
			b.logf("%s confirmed with the passkey %s", device.Name, key.Name)
			// A device the passkey made the person's, or now names another
			// passkey, reconnects, so its daemon connection carries it.
			reply(map[string]any{"device": device.Name, "promoted": device.Passkey != key.Name})
		default:
			token, err := b.devices.SignIn(p.Device, key.Name)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			b.signIn(w, r, token)
			b.touch(digest(token))
			b.logf("%s signed in with the passkey %s", p.Device, key.Name)
			reply(map[string]any{"device": p.Device, "promoted": true})
		}
	}
}

// asserted checks an assertion answering challenge at host and returns the
// registered passkey that made it.
func (b *bridge) asserted(challenge, id, clientData64, authData64, signature64, host, origin string) (Passkey, error) {
	clientData, err1 := unb64(clientData64)
	authData, err2 := unb64(authData64)
	signature, err3 := unb64(signature64)
	if errors.Join(err1, err2, err3) != nil || !b.answered(challenge, "webauthn.get", host) {
		return Passkey{}, errPasskey
	}
	keys, err := b.devices.Passkeys()
	if err != nil {
		return Passkey{}, err
	}
	for _, k := range keys {
		if k.ID != id {
			continue
		}
		spki, err := unb64(k.PublicKey)
		if err != nil {
			return Passkey{}, errPasskey
		}
		key, err := passkeyKey(spki)
		if err != nil || checkClientData(clientData, "webauthn.get", challenge, origin) != nil || checkAuthenticatorData(authData, hostName(host)) != nil || verifyAssertion(key, authData, clientData, signature) != nil {
			return Passkey{}, errPasskey
		}
		return k, nil
	}
	return Passkey{}, errors.New("that passkey is not registered here: it may have been removed")
}
