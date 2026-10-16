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
	// such as a tailnet name; one written https://NAME is reached over https
	// through a proxy that ends TLS in front of the console (tailscale
	// serve), so its browsers' origin is https although the listener is not.
	// TLS says the listener speaks https.
	Devices *Devices
	Hosts   []string
	TLS     bool
}

type bridge struct {
	readOnly     bool
	ctx          context.Context
	socket, code string
	// session is what this computer's paired browser holds in its cookie:
	// random for each run and good only until it stops, never the pairing
	// code, which a browser sends to every other server on the loopback
	// address and which could pair a device for months.
	session string
	devices *Devices
	extra   []string
	tls     bool
	local   string          // the listener's address
	hosts   map[string]bool // Host values this console answers to
	https   map[string]bool // the hosts whose browsers reach it over https through a proxy

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
	session, _ := PairingCode() // crypto/rand does not fail
	return &Server{b: &bridge{socket: options.Socket, code: options.PairingCode, session: session, readOnly: options.ReadOnly,
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
	b.ctx = ctx
	b.listensAt(listener.Addr().String())
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

// listensAt gives the bridge the address it listens on and the names it
// answers to: that address, and Options.Hosts.
func (b *bridge) listensAt(addr string) {
	b.local = addr
	b.hosts, b.https = map[string]bool{addr: true}, map[string]bool{}
	for _, h := range b.extra {
		if name, ok := strings.CutPrefix(h, "https://"); ok {
			h = name
			b.https[h] = true
		}
		b.hosts[h] = true
	}
}

// secure says the browser that sent a request to host reached it over https:
// the listener speaks it, or a proxy in front does and the host was named so.
func (b *bridge) secure(host string) bool { return b.tls || b.https[host] }

// scheme is the scheme of the origin a page served for host has.
func (b *bridge) scheme(host string) string {
	if b.secure(host) {
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
	if b.secure(r.Host) {
		socketScheme = "wss"
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self' "+socketScheme+"://"+r.Host+"; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	origin := b.scheme(r.Host) + "://" + r.Host
	if o := r.Header.Get("Origin"); o != "" && o != origin {
		// Say what was expected: behind a proxy that ends TLS the browser's
		// origin is https although this console speaks http, which --name
		// https://NAME tells it.
		http.Error(w, "Invalid origin: expected "+origin, http.StatusForbidden)
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
	return "", err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(b.session)) == 1
}

// deviceLife is how long a paired device stays paired without pairing again.
const deviceLife = 180 * 24 * time.Hour

func (b *bridge) pair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != b.scheme(r.Host)+"://"+r.Host || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
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
		http.SetCookie(w, &http.Cookie{Name: deviceCookie, Value: token, Path: "/", HttpOnly: true, Secure: b.secure(r.Host), SameSite: http.SameSiteStrictMode, MaxAge: int(deviceLife / time.Second)})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// This machine: HttpOnly keeps the capability out of script; strict
	// origin and host checks protect it from pages on another loopback
	// port. The browser still sends it to every server on this address, so
	// it is the run's session, not the code.
	http.SetCookie(w, &http.Cookie{Name: b.cookieName(), Value: b.session, Path: "/", HttpOnly: true, Secure: b.secure(r.Host), SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func readOnly(method string) bool {
	switch method {
	case "schedule.read", "schedule.next", "hello", "session.list", "session.watch", "session.info", "session.totals", "session.subscribe", "session.unsubscribe", "log.read", "log.runs", "conversation.search", "log.context",
		"attention.list", "notes.read", "index.status", "models.status", "models.offered", "models.published", "keychain.status", "settings.read",
		"workspace.list", "workspace.forgotten", "versions.list", "versions.diff", "worktree.diff", "mcp.list", "mcp.runtime", "credentials.list":
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

// maxMessage is the largest message the console may send over its socket. It
// is above the daemon's frame limit, so a request that is too large for the
// daemon is answered with an error that says so; a socket that closes at the
// limit instead drops the connection and every call waiting on it.
const maxMessage = 4 << 20

// maxRequest is the largest request the daemon takes (rpc's frame limit).
const maxRequest = 1 << 20

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
		// A refused handshake is all a browser learns of an HTTP error,
		// which the console reads as "not paired": the socket opens and
		// closes saying why instead.
		if conn, err := websocket.Accept(w, r, nil); err == nil {
			conn.Close(websocket.StatusTryAgainLater, "nodwyn is not running on this computer")
		}
		return
	}
	defer client.Close()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxMessage)
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
		case len(req.Params) >= maxRequest:
			callErr = errors.New("that message is too large to send: a request is at most 1 MB")
		case !b.allowed(req.Method, remote):
			callErr = errors.New("operation unavailable in this browser mode; use the terminal client")
			if b.readOnly {
				callErr = errors.New("this browser view is read-only; use the terminal client for this operation")
			} else if remote {
				callErr = errors.New("this is not available from another device; use nodwyn on its own computer")
			}
		case strings.HasPrefix(req.Method, "devices."):
			result, callErr = b.deviceMethod(req)
		case slowCalls[req.Method] > 0:
			// Answered when it is done; the console's next requests are
			// read meanwhile.
			go func(req request) {
				result, err := b.slowCall(ctx, via, req)
				if write(ctx, conn, reply(req.ID, result, err)) != nil {
					cancel()
				}
			}(req)
			continue
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
		if err := write(ctx, conn, reply(req.ID, result, callErr)); err != nil {
			return
		}
	}
}

// reply is a relayed call's answer.
func reply(id uint64, result json.RawMessage, err error) map[string]any {
	r := map[string]any{"jsonrpc": "2.0", "id": id}
	if err != nil {
		r["error"] = map[string]any{"code": -32000, "message": err.Error()}
	} else {
		r["result"] = result
	}
	return r
}

// slowCalls are the methods that may take longer than a relayed call's 30
// seconds, each with a little more than the daemon allows it (an image pull
// thirty minutes, a model's test a minute). The daemon answers a connection's
// requests one at a time, so each runs on a connection of its own: on the
// console's, a pull would hold up an abort or an answer for minutes.
var slowCalls = map[string]time.Duration{
	"mcp.pull":    31 * time.Minute,
	"models.test": 65 * time.Second,
}

// slowCall relays one of slowCalls over a daemon connection of its own.
func (b *bridge) slowCall(ctx context.Context, via string, req request) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, slowCalls[req.Method])
	defer cancel()
	client, err := rpc.DialAs(ctx, b.socket, via)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	var result json.RawMessage
	err = client.Call(ctx, req.Method, req.Params, &result)
	return result, err
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
	case "session.open", "worktree.open", "worktree.review", "session.resume", "session.send", "session.abort", "session.close", "session.answer", "session.approval.create", "session.friction", "session.policy.read",
		"schedule.pause", "schedule.run", "session.notes.write", "session.restore", "session.rename", "session.archive":
		return true
	// "Don't ask" can be turned off from a device, the safe direction; the
	// daemon refuses to turn it on for one (ADR 30), so a device can stop a
	// task that is skipping its questions without aborting it.
	case "session.approve_all":
		return true
	// Choosing a conversation's model, or the judge its plans and commands
	// go to, decides where its content goes next, so it stays with the
	// computer nodwyn runs on (ADR 30, ADR 40, ADR 47).
	case "session.model.set", "session.judge.set":
		return !remote
	// A task's turn limits are what stops a turn nobody is watching, so
	// lifting them stays with this computer too, as Settings' limits do
	// (ADR 49); the daemon refuses limits: false in session.open from a
	// device.
	case "schedule.set", "session.limits.set":
		return !remote
	// A log is the record of what a session did, devices included: only
	// this computer removes one (ADR 39).
	case "session.delete":
		return !remote
	case "session.binding.create", "session.policy.write", "keychain.create", "keychain.unlock", "settings.control.write", "settings.docs.write", "settings.write",
		"settings.provider.write", "settings.provider.remove", "settings.model.write", "settings.model.remove", "models.test",
		"workspace.save", "workspace.active", "workspace.remove", "index.forget", "mcp.inspect", "mcp.save", "mcp.approve", "mcp.remove", "mcp.pull":
		return !remote
	// Keys and the passphrase are typed here, and an OAuth2 provider sends
	// the person back to a listener on this computer's loopback. A key the
	// model asks for (session.secret) is a key typed too: a device shows the
	// request, and it is answered here.
	case "keychain.passphrase", "credentials.add", "credentials.replace", "credentials.remove", "credentials.bind", "credentials.unbind", "credentials.authorize", "session.secret":
		return !remote
	case "devices.list", "devices.revoke":
		return !remote && b.devices != nil
	}
	return false
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
