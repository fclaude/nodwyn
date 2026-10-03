// Package web serves the browser frontend over an authenticated loopback bridge.
// The existing Unix-socket daemon remains the sole owner of sessions and policy.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
)

//go:embed assets/*
var assets embed.FS

// PairingCode returns a fresh random code for pairing a browser with Serve.
func PairingCode() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// Options configure the browser console.
type Options struct {
	Socket      string // the daemon the browser's requests are relayed to
	PairingCode string // printed in the terminal; a browser presents it once
	ReadOnly    bool   // allow only listing, watching and reading logs

	// Remote pairing (ADR 30). Devices remembers browsers paired from other
	// machines; without it only this machine's browsers can pair. Hosts are
	// names (host:port) the console answers to besides its listener address,
	// such as a tailnet name. TLS says the listener speaks https.
	Devices *Devices
	Hosts   []string
	TLS     bool
}

type bridge struct {
	readOnly     bool
	ctx          context.Context
	socket, code string
	devices      *Devices
	extra        []string
	tls          bool
	local        string          // the listener's address
	hosts        map[string]bool // Host values this console answers to

	mu      sync.Mutex
	tickets map[string]time.Time // launch tickets not yet used, by value, with their expiry
	recheck time.Duration        // how often an open device console rechecks its pairing; 0 is deviceRecheck
}

// Server is a browser console not yet serving: it can mint launch tickets
// for the page its launcher opens before Serve starts.
type Server struct {
	b *bridge
}

// New prepares a console for options.
func New(options Options) *Server {
	return &Server{b: &bridge{socket: options.Socket, code: options.PairingCode, readOnly: options.ReadOnly,
		devices: options.Devices, extra: options.Hosts, tls: options.TLS, tickets: map[string]time.Time{}}}
}

// ticketLife bounds how long a launch ticket waits for its page to load.
const ticketLife = time.Minute

// Ticket mints a single-use launch ticket (ADR 30): the launcher puts it in
// the URL fragment of the page it opens, which browsers never send to a
// server; the page posts it to /pair once. It pairs like the code does, and
// only for a browser on this machine.
func (s *Server) Ticket() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	ticket := base64.RawURLEncoding.EncodeToString(raw[:])
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := time.Now()
	for t, expiry := range s.b.tickets {
		if now.After(expiry) {
			delete(s.b.tickets, t)
		}
	}
	s.b.tickets[ticket] = now.Add(ticketLife)
	return ticket, nil
}

// redeem spends a ticket: true once, for a live ticket.
func (b *bridge) redeem(ticket string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for t, expiry := range b.tickets {
		if subtle.ConstantTimeCompare([]byte(t), []byte(ticket)) == 1 {
			delete(b.tickets, t)
			return time.Now().Before(expiry)
		}
	}
	return false
}

// Serve runs a console on listener; see Server.Serve.
func Serve(ctx context.Context, listener net.Listener, options Options) error {
	return New(options).Serve(ctx, listener)
}

// Serve runs the console on listener until ctx ends. A loopback listener
// serves this machine's browsers; any other address, or extra host names,
// also serve remote ones, and so require device pairing. The pairing code
// travels through the terminal and a same-origin POST, never a URL,
// script-accessible storage or an RPC log.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	b := s.b
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || len(b.code) < 32 {
		return fmt.Errorf("web: requires a TCP listener and a strong pairing code")
	}
	if (!addr.IP.IsLoopback() || len(b.extra) > 0) && b.devices == nil {
		return fmt.Errorf("web: a console reachable from other machines needs device pairing")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.ctx, b.local = ctx, listener.Addr().String()
	b.hosts = map[string]bool{b.local: true}
	for _, h := range b.extra {
		b.hosts[h] = true
	}
	server := &http.Server{Handler: b, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			server.Close()
		case <-done:
		}
	}()
	defer close(done)
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return err
}

func (b *bridge) scheme() string {
	if b.tls {
		return "https"
	}
	return "http"
}

// remote reports whether a request comes from another machine, or reaches
// this console by a name other than its loopback address (a proxy such as
// tailscale serve connects from loopback but on a phone's behalf).
func (b *bridge) remote(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() || r.Host != b.local {
		return true
	}
	listener, _, _ := net.SplitHostPort(b.local)
	return !net.ParseIP(listener).IsLoopback()
}

// remoteFor is remote for a paired request: a device token is another
// machine's authority wherever the request comes from.
func (b *bridge) remoteFor(r *http.Request, device string) bool {
	return device != "" || b.remote(r)
}

func (b *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if !b.hosts[r.Host] {
		http.Error(w, "Invalid host", http.StatusForbidden)
		return
	}
	socketScheme := "ws"
	if b.tls {
		socketScheme = "wss"
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self' "+socketScheme+"://"+r.Host+"; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	origin := b.scheme() + "://" + r.Host
	if o := r.Header.Get("Origin"); o != "" && o != origin {
		http.Error(w, "Invalid origin", http.StatusForbidden)
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		http.Error(w, "Cross-origin request refused", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/pair":
		b.pair(w, r)
	case "/attach":
		device, ok := b.authenticated(r)
		if r.Method != http.MethodPost || r.Header.Get("Origin") != origin || !ok || b.readOnly {
			http.Error(w, "Pair this browser first", http.StatusForbidden)
			return
		}
		b.attach(w, r, device)
	case "/socket":
		device, ok := b.authenticated(r)
		if r.Method != http.MethodGet || r.Header.Get("Origin") != origin || !ok {
			http.Error(w, "Pair this browser first", http.StatusForbidden)
			return
		}
		token := ""
		if c, err := r.Cookie(deviceCookie); err == nil && device != "" {
			token = c.Value
		}
		b.connect(w, r, device, token)
	default:
		b.asset(w, r)
	}
}

// asset serves the embedded console: a fixed file set compiled into the
// binary, so a path either names one of those files or is not found. There
// are no directory listings and nothing from the filesystem.
func (b *bridge) asset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	files, _ := fs.Sub(assets, "assets")
	if info, err := fs.Stat(files, name); err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(name, "fonts/") {
		// Fonts never change within a build; everything else stays no-store.
		w.Header().Set("Cache-Control", "max-age=86400")
	}
	http.ServeFileFS(w, r, files, name)
}

func (b *bridge) cookieName() string { return "nodwyn_ui_" + strings.ReplaceAll(b.local, ":", "_") }

// deviceCookie is shared by every name this console answers to: a device
// stays paired across restarts, which change the pairing code and port.
const deviceCookie = "nodwyn_device"

// authenticated returns the paired device's name, "" for this machine's
// browser, and whether the request is paired at all. This machine's pairing
// holds only for requests that are not remote.
func (b *bridge) authenticated(r *http.Request) (string, bool) {
	if b.devices != nil {
		if c, err := r.Cookie(deviceCookie); err == nil {
			if name, ok := b.devices.Check(c.Value); ok {
				return name, true
			}
		}
	}
	if b.remote(r) {
		return "", false
	}
	cookie, err := r.Cookie(b.cookieName())
	return "", err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(b.code)) == 1
}

// deviceLife is how long a paired device stays paired without pairing again.
const deviceLife = 180 * 24 * time.Hour

func (b *bridge) pair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != b.scheme()+"://"+r.Host || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "Same-origin JSON POST required", http.StatusForbidden)
		return
	}
	var p struct {
		Code   string `json:"code"`
		Ticket string `json:"ticket"`
		Device string `json:"device"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		http.Error(w, "Invalid pairing request", http.StatusForbidden)
		return
	}
	remote := b.remote(r)
	switch {
	case p.Ticket != "" && !remote:
		if !b.redeem(p.Ticket) {
			http.Error(w, "This launch link was already used or has expired. Paste the pairing code instead.", http.StatusForbidden)
			return
		}
	case p.Ticket != "":
		http.Error(w, "A launch link pairs only a browser on this computer", http.StatusForbidden)
		return
	case subtle.ConstantTimeCompare([]byte(p.Code), []byte(b.code)) != 1:
		http.Error(w, "Invalid pairing code", http.StatusForbidden)
		return
	}
	if remote || p.Device != "" {
		// Another machine pairs as a named device: its answers carry the
		// name, and it can be revoked without restarting anything.
		if b.devices == nil {
			http.Error(w, "Device pairing is not enabled", http.StatusForbidden)
			return
		}
		if p.Device == "" {
			http.Error(w, "Name this device, for example phone", http.StatusBadRequest)
			return
		}
		token, err := b.devices.Pair(p.Device)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: deviceCookie, Value: token, Path: "/", HttpOnly: true, Secure: b.tls, SameSite: http.SameSiteStrictMode, MaxAge: int(deviceLife / time.Second)})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// This machine: HttpOnly keeps the capability out of script; strict
	// origin and host checks protect it even from another loopback port.
	http.SetCookie(w, &http.Cookie{Name: b.cookieName(), Value: b.code, Path: "/", HttpOnly: true, Secure: b.tls, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func readOnly(method string) bool {
	switch method {
	case "hello", "session.list", "session.watch", "session.info", "session.subscribe", "session.unsubscribe", "log.read",
		"attention.list", "notes.read", "models.status", "run.info", "keychain.status", "settings.read",
		"workspace.list", "versions.list", "mcp.list", "mcp.runtime", "credentials.list":
		return true
	}
	return false
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// deviceRecheck is how often an open console of a paired device checks that
// the device is still paired, so a revocation also ends a console that is
// only watching.
const deviceRecheck = 5 * time.Second

func (b *bridge) connect(w http.ResponseWriter, r *http.Request, device, token string) {
	ctx, cancel := context.WithCancel(b.ctx)
	defer cancel()
	// A device token is another machine's authority wherever it arrives
	// from: only this machine's pairing grants local powers.
	remote := b.remoteFor(r, device)
	via := ""
	if device != "" {
		via = "web:" + device
	}
	dialCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	client, err := rpc.DialAs(dialCtx, b.socket, via)
	stop()
	if err != nil {
		http.Error(w, "Daemon unavailable", http.StatusServiceUnavailable)
		return
	}
	defer client.Close()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	// A paired device stays paired only while its token is in devices.toml:
	// revoking it ends this console within deviceRecheck, and before any
	// further request is relayed.
	paired := func() bool {
		if device == "" {
			return true
		}
		name, ok := b.devices.Check(token)
		return ok && name == device
	}
	revoked := func() {
		conn.Close(websocket.StatusPolicyViolation, "this device was revoked")
		cancel()
	}
	if device != "" {
		go func() {
			every := b.recheck
			if every <= 0 {
				every = deviceRecheck
			}
			tick := time.NewTicker(every)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					if !paired() {
						revoked()
						return
					}
				}
			}
		}()
	}
	// Browser backpressure cannot block a session: the RPC client and daemon
	// already enforce bounded queues. A failed connection is never replayed.
	go func() {
		defer cancel()
		for {
			ev, err := client.Next(ctx)
			if err != nil {
				return
			}
			message := struct {
				JSONRPC string          `json:"jsonrpc"`
				Method  string          `json:"method"`
				Params  rpc.ClientEvent `json:"params"`
			}{"2.0", "session.event", ev}
			if err := write(ctx, conn, message); err != nil {
				return
			}
		}
	}()
	for {
		var req request
		if err := wsjson.Read(ctx, conn, &req); err != nil {
			return
		}
		if !paired() {
			revoked()
			return
		}
		var result json.RawMessage
		var callErr error
		switch {
		case req.JSONRPC != "2.0" || req.ID == 0:
			callErr = errors.New("invalid request")
		case !b.allowed(req.Method, remote) || (remote && unwatchedRun(req)):
			callErr = errors.New("operation unavailable in this browser mode; use the terminal client")
			if b.readOnly {
				callErr = errors.New("this browser view is read-only; use the terminal client for this operation")
			} else if remote && unwatchedRun(req) {
				callErr = errors.New("a run whose requests nobody answers by hand starts only on the computer nodwyn runs on")
			} else if remote {
				callErr = errors.New("this is not available from another device; use nodwyn on its own computer")
			}
		case strings.HasPrefix(req.Method, "devices."):
			result, callErr = b.deviceMethod(req)
		default:
			callCtx, stop := context.WithTimeout(ctx, 30*time.Second)
			if req.Method == "hello" {
				var hello rpc.HelloResult
				callErr = client.Call(callCtx, req.Method, req.Params, &hello)
				if callErr == nil {
					methods := []string{}
					for _, method := range append(hello.Methods, "devices.list", "devices.revoke") {
						if b.allowed(method, remote) {
							methods = append(methods, method)
						}
					}
					hello.Methods = methods
					result, callErr = json.Marshal(hello)
				}
			} else {
				callErr = client.Call(callCtx, req.Method, req.Params, &result)
			}
			stop()
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if callErr != nil {
			reply["error"] = map[string]any{"code": -32000, "message": callErr.Error()}
		} else {
			reply["result"] = result
		}
		if err := write(ctx, conn, reply); err != nil {
			return
		}
	}
}
func write(ctx context.Context, c *websocket.Conn, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(ctx, c, v)
}

// Only operations with a complete browser view join this allowlist.
// Core remains authoritative for every answer and its live prompt ID.
// Another device answers and steers, but never widens policy or changes
// nodwyn's setup (ADR 30): those stay with the computer nodwyn runs on.
func (b *bridge) allowed(method string, remote bool) bool {
	if readOnly(method) {
		return true
	}
	if b.readOnly {
		return false
	}
	switch method {
	case "session.open", "session.resume", "session.send", "session.abort", "session.close", "session.answer", "session.secret", "session.approval.create", "session.friction", "session.policy.read",
		"session.notes.write", "run.start", "run.stop", "run.resume", "session.restore", "session.rename", "session.archive":
		return true
	// A log is the record of what a session did, devices included: only
	// this computer removes one (ADR 39).
	case "session.delete":
		return !remote
	case "session.binding.create", "session.approve_all", "session.policy.write", "keychain.create", "keychain.unlock", "settings.control.write", "settings.docs.write", "settings.write",
		"workspace.save", "workspace.remove", "mcp.inspect", "mcp.save", "mcp.approve", "mcp.remove", "mcp.pull":
		return !remote
	// Keys and the passphrase are typed here, and an OAuth2 provider sends
	// the person back to a listener on this computer's loopback.
	case "keychain.passphrase", "credentials.add", "credentials.replace", "credentials.remove", "credentials.bind", "credentials.authorize":
		return !remote
	case "devices.list", "devices.revoke":
		return !remote && b.devices != nil
	}
	return false
}

// unwatchedRun reports a run.start or run.resume whose requests a person
// would not answer: another device may start or resume a run only as one
// it answers itself. The daemon refuses it too, by the device's via.
func unwatchedRun(req request) bool {
	if req.Method != "run.start" && req.Method != "run.resume" {
		return false
	}
	var p struct {
		AutoApprove bool `json:"auto_approve"`
		Unattended  bool `json:"unattended"`
		Judge       bool `json:"judge"`
	}
	// Params the daemon would refuse anyway are left to it.
	_ = json.Unmarshal(req.Params, &p)
	return p.AutoApprove || p.Unattended || p.Judge
}

// maxUpload bounds one attached file.
const maxUpload = 20 << 20

// attach receives one file for a session (POST /attach?id=SESSION&name=NAME,
// the file as the body). The bridge stages it in a private directory, asks
// the daemon to attach that path, and removes it: a page never names a path
// the daemon reads.
func (b *bridge) attach(w http.ResponseWriter, r *http.Request, device string) {
	id, name := r.URL.Query().Get("id"), r.URL.Query().Get("name")
	if id == "" || name == "" || len(name) > 255 {
		http.Error(w, "id and name are required", http.StatusBadRequest)
		return
	}
	dir, err := os.MkdirTemp("", "nodwyn-upload-")
	if err != nil {
		http.Error(w, "Could not receive the file", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(dir)
	staged := filepath.Join(dir, "upload")
	f, err := os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		http.Error(w, "Could not receive the file", http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(f, http.MaxBytesReader(w, r.Body, maxUpload))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("The file is larger than %d MB or did not arrive whole", maxUpload>>20), http.StatusRequestEntityTooLarge)
		return
	}
	via := ""
	if device != "" {
		via = "web:" + device
	}
	// An image waits for its confirm, which the daemon gives up on after
	// ten minutes; the call waits a little longer than that.
	ctx, cancel := context.WithTimeout(b.ctx, 11*time.Minute)
	defer cancel()
	client, err := rpc.DialAs(ctx, b.socket, via)
	if err != nil {
		http.Error(w, "Daemon unavailable", http.StatusServiceUnavailable)
		return
	}
	defer client.Close()
	var receipt json.RawMessage
	if err := client.Call(ctx, "session.attach", map[string]string{"id": id, "path": staged, "name": name}, &receipt); err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(receipt)
}

// deviceMethod answers the console's own methods, which the daemon does
// not know: the devices paired with this console.
func (b *bridge) deviceMethod(req request) (json.RawMessage, error) {
	if req.Method == "devices.list" {
		devices, err := b.devices.List()
		if err != nil {
			return nil, err
		}
		if devices == nil {
			devices = []Device{}
		}
		return json.Marshal(devices)
	}
	var p struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(req.Params)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil || p.Name == "" {
		return nil, errors.New("name is required")
	}
	if err := b.devices.Revoke(p.Name); err != nil {
		return nil, err
	}
	return json.RawMessage("{}"), nil
}
