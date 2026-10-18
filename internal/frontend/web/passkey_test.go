package web

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
)

// authenticator is a software passkey: what a phone's or a laptop's does,
// with a key in memory, for a person who is always there and verified.
type authenticator struct {
	key   crypto.Signer
	id    string
	flags byte // the authenticator data's flags; 0 is present and verified
}

func newAuthenticator(t *testing.T) *authenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &authenticator{key: key, id: b64(random(16))}
}

func b64(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

func (a *authenticator) authenticatorData(rpID string) []byte {
	flags := a.flags
	if flags == 0 {
		flags = flagPresent | flagVerified
	}
	hash := sha256.Sum256([]byte(rpID))
	return append(hash[:], flags, 0, 0, 0, 1)
}

func clientDataFor(kind, challenge, origin string) []byte {
	raw, _ := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return raw
}

// register answers a registration's challenge as navigator.credentials.create
// would, with the fields the console sends.
func (a *authenticator) register(t *testing.T, rpID, origin, challenge string) map[string]string {
	t.Helper()
	spki, err := x509.MarshalPKIXPublicKey(a.key.Public())
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"challenge": challenge, "id": a.id, "client_data": b64(clientDataFor("webauthn.create", challenge, origin)),
		"authenticator_data": b64(a.authenticatorData(rpID)), "public_key": b64(spki)}
}

// assert answers a sign-in's or a touch's challenge as
// navigator.credentials.get would.
func (a *authenticator) assert(t *testing.T, rpID, origin, challenge string) map[string]string {
	t.Helper()
	clientData := clientDataFor("webauthn.get", challenge, origin)
	authData := a.authenticatorData(rpID)
	hash := sha256.Sum256(clientData)
	signed := append(append([]byte{}, authData...), hash[:]...)
	var signature []byte
	var err error
	if key, ok := a.key.(ed25519.PrivateKey); ok {
		signature = ed25519.Sign(key, signed)
	} else {
		digest := sha256.Sum256(signed)
		signature, err = a.key.Sign(rand.Reader, digest[:], crypto.SHA256)
	}
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"challenge": challenge, "id": a.id, "client_data": b64(clientData), "authenticator_data": b64(authData), "signature": b64(signature)}
}

// browser is a browser at one origin of a console, keeping its cookies.
type browser struct {
	t       *testing.T
	b       *bridge
	host    string
	origin  string
	cookies []*http.Cookie
}

func (br *browser) post(path string, body any) *httptest.ResponseRecorder {
	br.t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", br.origin+path, bytes.NewReader(raw))
	r.Host = br.host
	r.RemoteAddr = "127.0.0.1:40000" // the proxy that ends TLS, on the device's behalf
	r.Header.Set("Origin", br.origin)
	r.Header.Set("Content-Type", "application/json")
	for _, c := range br.cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	br.b.ServeHTTP(w, r)
	if set := w.Result().Cookies(); len(set) > 0 {
		br.cookies = set
	}
	return w
}

// challenge asks /passkey/options for one, with an invitation or not.
func (br *browser) challenge(invite string) map[string]any {
	br.t.Helper()
	body := map[string]string{}
	if invite != "" {
		body["invite"] = invite
	}
	w := br.post("/passkey/options", body)
	if w.Code != http.StatusOK {
		br.t.Fatalf("options: %d %s", w.Code, w.Body)
	}
	var options map[string]any
	json.Unmarshal(w.Body.Bytes(), &options)
	return options
}

func sharedConsole(t *testing.T) (*Server, *Devices, *bytes.Buffer) {
	t.Helper()
	devices := OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))
	audit := &bytes.Buffer{}
	s := New(Options{PairingCode: testCode, Devices: devices, Hosts: []string{"https://desk.example"}, Audit: audit})
	s.b.ctx = context.Background()
	s.b.listensAt("127.0.0.1:7777")
	return s, devices, audit
}

// A passkey registered with an invitation from the terminal pairs the device
// as the person's; another browser then signs in with the same passkey, with
// no invitation; and every challenge, origin and flag is checked. A plain
// invitation registers no passkey, and an address has none.
func TestAPasskeyPairsThePersonsDevices(t *testing.T) {
	s, devices, audit := sharedConsole(t)
	laptop := &browser{t: t, b: s.b, host: "desk.example", origin: "https://desk.example"}
	key := newAuthenticator(t)

	plain, _, _ := devices.Invite(Terminal, false)
	if w := laptop.post("/passkey/options", map[string]string{"invite": plain}); w.Code != http.StatusForbidden {
		t.Fatalf("a plain invitation offered a registration: %d", w.Code)
	}
	if w := laptop.post("/passkey/options", map[string]string{}); w.Code != http.StatusForbidden {
		t.Fatalf("a sign-in offered with no passkey registered: %d", w.Code)
	}
	code, _, _ := devices.Invite(Terminal, true)
	options := laptop.challenge(code)
	if options["rp_id"] != "desk.example" {
		t.Fatalf("options: %v", options)
	}
	challenge := options["challenge"].(string)
	// What a page elsewhere, or a passkey for another name, answers.
	for name, answer := range map[string]map[string]string{
		"another origin": key.register(t, "desk.example", "https://evil.example", challenge),
		"another RP ID":  key.register(t, "evil.example", "https://desk.example", challenge),
	} {
		body := map[string]string{"invite": code, "device": "laptop"}
		for k, v := range answer {
			body[k] = v
		}
		if w := laptop.post("/passkey/register", body); w.Code != http.StatusForbidden {
			t.Fatalf("%s: %d", name, w.Code)
		}
		// A refused answer spends its challenge.
		challenge = laptop.challenge(code)["challenge"].(string)
	}
	body := map[string]string{"invite": code, "device": "laptop"}
	for k, v := range key.register(t, "desk.example", "https://desk.example", challenge) {
		body[k] = v
	}
	if w := laptop.post("/passkey/register", body); w.Code != http.StatusOK || len(laptop.cookies) != 1 || !laptop.cookies[0].Secure {
		t.Fatalf("registration: %d %s %v", w.Code, w.Body, laptop.cookies)
	}
	if w := laptop.post("/passkey/register", body); w.Code != http.StatusForbidden {
		t.Fatalf("a registration answered twice: %d", w.Code)
	}
	if d, ok := devices.Check(laptop.cookies[0].Value); !ok || d.Name != "laptop" || d.Passkey != "laptop" {
		t.Fatalf("the laptop: %+v %v", d, ok)
	}
	if !s.b.touched(digest(laptop.cookies[0].Value)) {
		t.Fatal("registering is no touch")
	}

	// Another browser signs in with the passkey alone.
	tablet := &browser{t: t, b: s.b, host: "desk.example", origin: "https://desk.example"}
	options = tablet.challenge("")
	if ids, _ := options["credentials"].([]any); len(ids) != 1 || ids[0] != key.id {
		t.Fatalf("sign-in options: %v", options)
	}
	challenge = options["challenge"].(string)
	unverified := *key
	unverified.flags = flagPresent
	answer := unverified.assert(t, "desk.example", "https://desk.example", challenge)
	answer["device"] = "tablet"
	if w := tablet.post("/passkey/assert", answer); w.Code != http.StatusForbidden {
		t.Fatalf("a passkey whose person was not verified: %d", w.Code)
	}
	other := newAuthenticator(t)
	challenge = tablet.challenge("")["challenge"].(string)
	answer = other.assert(t, "desk.example", "https://desk.example", challenge)
	answer["device"] = "tablet"
	if w := tablet.post("/passkey/assert", answer); w.Code != http.StatusForbidden {
		t.Fatalf("an unregistered passkey: %d", w.Code)
	}
	forged := *key
	forged.key = other.key
	challenge = tablet.challenge("")["challenge"].(string)
	answer = forged.assert(t, "desk.example", "https://desk.example", challenge)
	answer["device"] = "tablet"
	if w := tablet.post("/passkey/assert", answer); w.Code != http.StatusForbidden {
		t.Fatalf("a registered id signed with another key: %d", w.Code)
	}
	challenge = tablet.challenge("")["challenge"].(string)
	answer = key.assert(t, "desk.example", "https://desk.example", challenge)
	answer["device"] = "tablet"
	if w := tablet.post("/passkey/assert", answer); w.Code != http.StatusOK || len(tablet.cookies) != 1 {
		t.Fatalf("sign-in: %d %s", w.Code, w.Body)
	}
	if d, ok := devices.Check(tablet.cookies[0].Value); !ok || d.Passkey != "laptop" || d.By != "passkey:laptop" {
		t.Fatalf("the tablet: %+v", d)
	}
	for _, line := range []string{"web: laptop paired with an invitation and registered the passkey laptop", "web: tablet signed in with the passkey laptop"} {
		if !strings.Contains(audit.String(), line) {
			t.Fatalf("the audit lacks %q:\n%s", line, audit)
		}
	}

	// A passkey removed signs nothing in, and the devices it signed in
	// only answer. The last one stays.
	if err := devices.RemovePasskey("laptop"); err == nil {
		t.Fatal("the last passkey was removed")
	}
	second, _, _ := devices.Invite("laptop", true)
	phone := &browser{t: t, b: s.b, host: "desk.example", origin: "https://desk.example"}
	phoneKey := newAuthenticator(t)
	body = map[string]string{"invite": second, "device": "phone"}
	for k, v := range phoneKey.register(t, "desk.example", "https://desk.example", phone.challenge(second)["challenge"].(string)) {
		body[k] = v
	}
	if w := phone.post("/passkey/register", body); w.Code != http.StatusOK {
		t.Fatalf("a second passkey: %d %s", w.Code, w.Body)
	}
	if err := devices.RemovePasskey("laptop"); err != nil {
		t.Fatal(err)
	}
	if d, ok := devices.Check(tablet.cookies[0].Value); !ok || d.Passkey != "" {
		t.Fatalf("a device signed in with a removed passkey: %+v", d)
	}
	answer = key.assert(t, "desk.example", "https://desk.example", tablet.challenge("")["challenge"].(string))
	if w := tablet.post("/passkey/assert", answer); w.Code != http.StatusForbidden {
		t.Fatalf("a removed passkey touched: %d", w.Code)
	}

	// An address is no host name a passkey can be for, and http is not
	// https: neither offers a passkey.
	for _, at := range []*browser{
		{t: t, b: s.b, host: "127.0.0.1:7777", origin: "http://127.0.0.1:7777"},
	} {
		if w := at.post("/passkey/options", map[string]string{}); w.Code != http.StatusForbidden {
			t.Fatalf("passkeys at %s: %d", at.host, w.Code)
		}
	}
}

// An Ed25519 passkey checks out as a P-256 one does.
func TestAnEd25519PasskeyChecksOut(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := &authenticator{key: private, id: b64(random(16))}
	spki, _ := x509.MarshalPKIXPublicKey(private.Public())
	public, err := passkeyKey(spki)
	if err != nil {
		t.Fatal(err)
	}
	answer := key.assert(t, "desk.example", "https://desk.example", "c")
	clientData, _ := unb64(answer["client_data"])
	authData, _ := unb64(answer["authenticator_data"])
	signature, _ := unb64(answer["signature"])
	if err := verifyAssertion(public, authData, clientData, signature); err != nil {
		t.Fatal(err)
	}
	signature[0] ^= 1
	if verifyAssertion(public, authData, clientData, signature) == nil {
		t.Fatal("a changed signature checked out")
	}
}

// Over the console's socket, the person's device makes this computer's
// choices at once (the daemon takes them, recording the passkey), and a
// setup action waits for a touch: it is refused with touchCode, which the
// console answers by asking for the passkey, and after the touch it goes
// through, said in the audit. A device paired by a plain invitation is
// offered neither.
func TestThePersonsDeviceTouchesItsPasskeyForSetup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "nodwyn-web-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	socket := filepath.Join(root, "run", "sock")
	listener, err := rpc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	done := make(chan error, 1)
	go func() { done <- rpc.Serve(ctx, listener, core.Options{Config: config.Default(), Paths: paths}) }()
	defer func() { cancel(); <-done }()

	devices := OpenDevices(filepath.Join(root, "devices.toml"))
	audit := &bytes.Buffer{}
	b := &bridge{ctx: ctx, socket: socket, code: testCode, session: testSession, devices: devices, audit: audit, extra: []string{"https://desk.example"}}
	server := httptest.NewUnstartedServer(b)
	b.listensAt(server.Listener.Addr().String())
	server.Start()
	defer server.Close()

	key := newAuthenticator(t)
	laptop := &browser{t: t, b: b, host: "desk.example", origin: "https://desk.example"}
	code, _, _ := devices.Invite(Terminal, true)
	body := map[string]string{"invite": code, "device": "laptop"}
	for k, v := range key.register(t, "desk.example", "https://desk.example", laptop.challenge(code)["challenge"].(string)) {
		body[k] = v
	}
	if w := laptop.post("/passkey/register", body); w.Code != http.StatusOK {
		t.Fatalf("registration: %d %s", w.Code, w.Body)
	}
	plain, _, _ := devices.Invite(Terminal, false)
	phoneToken, err := devices.Join(plain, "phone")
	if err != nil {
		t.Fatal(err)
	}
	open := func(token string) (*websocket.Conn, *http.Response) {
		t.Helper()
		header := http.Header{"Origin": {"https://desk.example"}, "Cookie": {deviceCookie + "=" + token}}
		conn, response, err := websocket.Dial(ctx, "ws://"+b.local+"/socket", &websocket.DialOptions{HTTPHeader: header, Host: "desk.example"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		return conn, response
	}
	type rpcError struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	id := 0
	call := func(conn *websocket.Conn, method string, params any) (json.RawMessage, *rpcError) {
		t.Helper()
		id++
		if err := wsjson.Write(ctx, conn, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		for {
			var frame struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *rpcError       `json:"error"`
			}
			if err := wsjson.Read(ctx, conn, &frame); err != nil {
				t.Fatal(err)
			}
			if frame.ID == id {
				return frame.Result, frame.Error
			}
		}
	}
	offered := func(conn *websocket.Conn) map[string]bool {
		t.Helper()
		raw, rerr := call(conn, "hello", map[string]any{"protocol": 1})
		if rerr != nil {
			t.Fatal(rerr)
		}
		var hello rpc.HelloResult
		json.Unmarshal(raw, &hello)
		methods := map[string]bool{}
		for _, m := range hello.Methods {
			methods[m] = true
		}
		methods["peer:"+hello.Peer] = true
		return methods
	}

	phone, _ := open(phoneToken)
	methods := offered(phone)
	if methods["settings.docs.write"] || methods["session.model.set"] || !methods["devices.invite"] {
		t.Fatalf("a device that only answers is offered %v", methods)
	}
	laptopConn, response := open(laptop.cookies[0].Value)
	// Connecting renews the sign-in.
	if renewed := response.Cookies(); len(renewed) != 1 || renewed[0].MaxAge != int(idleLife/time.Second) {
		t.Fatalf("the socket did not renew the sign-in: %v", renewed)
	}
	methods = offered(laptopConn)
	if !methods["settings.docs.write"] || !methods["session.model.set"] || !methods["devices.revoke"] {
		t.Fatalf("the person's device is offered %v", methods)
	}
	// A choice goes to the daemon, which takes it from a device that
	// carries a passkey: it answers about the task, not about the device.
	if _, rerr := call(laptopConn, "session.model.set", map[string]any{"id": "nope", "name": "x"}); rerr == nil || strings.Contains(rerr.Message, "paired device") {
		t.Fatalf("a choice from the person's device: %+v", rerr)
	}
	// The registration was a touch; one that lapsed is asked for again.
	b.mu.Lock()
	b.touches[digest(laptop.cookies[0].Value)] = time.Now().Add(-time.Second)
	b.mu.Unlock()
	setting := map[string]any{"google_upload": "deny"}
	if _, rerr := call(laptopConn, "settings.docs.write", setting); rerr == nil || rerr.Code != touchCode {
		t.Fatalf("a setup action before the touch: %+v", rerr)
	}
	if raw, _ := os.ReadFile(paths.ConfigFile); strings.Contains(string(raw), "google_upload") {
		t.Fatal("the setting was written before the touch")
	}
	if _, rerr := call(laptopConn, "devices.invite", map[string]any{"passkey": true}); rerr == nil || rerr.Code != touchCode {
		t.Fatalf("an invitation that may register a passkey before the touch: %+v", rerr)
	}
	answer := key.assert(t, "desk.example", "https://desk.example", laptop.challenge("")["challenge"].(string))
	w := laptop.post("/passkey/assert", answer)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"promoted":false`) {
		t.Fatalf("the touch: %d %s", w.Code, w.Body)
	}
	if _, rerr := call(laptopConn, "settings.docs.write", setting); rerr != nil {
		t.Fatalf("a setup action after the touch: %+v", rerr)
	}
	if raw, _ := os.ReadFile(paths.ConfigFile); !strings.Contains(string(raw), `google_upload = "deny"`) {
		t.Fatalf("config.toml:\n%s", raw)
	}
	if _, rerr := call(laptopConn, "devices.invite", map[string]any{"passkey": true}); rerr != nil {
		t.Fatalf("an invitation after the touch: %+v", rerr)
	}
	if !strings.Contains(audit.String(), "web: settings.docs.write by laptop with the passkey laptop: done") {
		t.Fatalf("the audit:\n%s", audit)
	}
	// The phone touches the passkey too, and is the person's from then on,
	// once it reconnects.
	phoneBrowser := &browser{t: t, b: b, host: "desk.example", origin: "https://desk.example", cookies: []*http.Cookie{{Name: deviceCookie, Value: phoneToken}}}
	answer = key.assert(t, "desk.example", "https://desk.example", phoneBrowser.challenge("")["challenge"].(string))
	if w := phoneBrowser.post("/passkey/assert", answer); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"promoted":true`) {
		t.Fatalf("the phone's touch: %d %s", w.Code, w.Body)
	}
	phone, _ = open(phoneToken)
	if methods := offered(phone); !methods["session.model.set"] || !strings.HasSuffix(peerOf(methods), " via web:phone passkey:laptop") {
		t.Fatalf("the phone after its touch is offered %v", methods)
	}
}

// peerOf is the peer that offered recorded.
func peerOf(methods map[string]bool) string {
	for m := range methods {
		if p, ok := strings.CutPrefix(m, "peer:"); ok {
			return p
		}
	}
	return ""
}
