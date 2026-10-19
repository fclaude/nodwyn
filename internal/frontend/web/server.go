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

	// Audit receives a line for each sign-in, invitation and setup action
	// of another device (ADR 30, amended 2026-10-17): on a computer nobody
	// sits at, the journal is where they are read. Nil writes none.
	Audit io.Writer
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

	audit io.Writer

	mu         sync.Mutex
	tickets    map[string]time.Time // launch tickets not yet used, by value, with their expiry
	ceremonies map[string]ceremony  // passkey challenges not yet answered, by value
	touches    map[string]time.Time // until when each device's last passkey touch lasts
	recheck    time.Duration        // how often an open device console rechecks its pairing; 0 is deviceRecheck
	ping       time.Duration        // how often an open console is pinged; 0 is pingEvery
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
		devices: options.Devices, extra: options.Hosts, tls: options.TLS, audit: options.Audit, tickets: map[string]time.Time{}}}
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
		if r.Method == http.MethodGet {
			b.pairing(w, r)
			return
		}
		b.pair(w, r)
	case "/passkey/options", "/passkey/register", "/passkey/assert":
		b.passkey(w, r)
	case "/attach":
		device, ok := b.authenticated(r)
		if r.Method != http.MethodPost || r.Header.Get("Origin") != origin || !ok || b.readOnly {
			http.Error(w, "Pair this browser first", http.StatusForbidden)
			return
		}
		b.attach(w, r, device)
	case "/paired":
		// A socket refused before it opened tells the console nothing of
		// why, so it asks here: a browser this console does not take pairs
		// again, and one that could not reach it tries again. It learns no
		// more than a refused socket would have told it.
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, ok := b.authenticated(r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"paired":%t}`, ok)
	case "/socket":
		device, ok := b.authenticated(r)
		if r.Method != http.MethodGet || r.Header.Get("Origin") != origin || !ok {
			http.Error(w, "Pair this browser first", http.StatusForbidden)
			return
		}
		token := ""
		if c, err := r.Cookie(deviceCookie); err == nil && device.Name != "" {
			token = c.Value
			b.signIn(w, r, token)
		}
		if host := r.URL.Query().Get("host"); host != "" {
			b.connectHost(w, r, device, token, host)
			return
		}
		b.connect(w, r, device, token)
	default:
		// The page renews a device's sign-in each time it loads, as the
		// socket does each time it connects (idleLife).
		if name := strings.TrimPrefix(r.URL.Path, "/"); name == "" || name == "index.html" {
			if c, err := r.Cookie(deviceCookie); err == nil && b.devices != nil {
				if _, ok := b.devices.Check(c.Value); ok {
					b.signIn(w, r, c.Value)
				}
			}
		}
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

// authenticated returns the paired device, the zero Device for this
// machine's browser, and whether the request is paired at all. This
// machine's pairing holds only for requests that are not remote.
func (b *bridge) authenticated(r *http.Request) (Device, bool) {
	if b.devices != nil {
		if c, err := r.Cookie(deviceCookie); err == nil {
			if device, ok := b.devices.Check(c.Value); ok {
				return device, true
			}
		}
	}
	if b.remote(r) {
		return Device{}, false
	}
	cookie, err := r.Cookie(b.cookieName())
	return Device{}, err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(b.session)) == 1
}

// signIn gives a device's browser the cookie that holds its token, good for
// idleLife from now: set on pairing, and again on every load and connection
// of its console, so a device in use stays signed in.
func (b *bridge) signIn(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: deviceCookie, Value: token, Path: "/", HttpOnly: true, Secure: b.secure(r.Host), SameSite: http.SameSiteStrictMode, MaxAge: int(idleLife / time.Second)})
}

// shared reports whether other machines reach this console, which then pairs
// them by invitation and takes no pairing code at all (ADR 30, amended
// 2026-10-17): on a computer nobody sits at the code sat in the journal for
// as long as the console ran, and paired any number of devices, or, with
// the console's own loopback address, this computer.
func (b *bridge) shared() bool { return b.devices != nil }

// logf writes an audit line (Options.Audit).
func (b *bridge) logf(format string, args ...any) {
	if b.audit != nil {
		fmt.Fprintf(b.audit, "web: "+format+"\n", args...)
	}
}

// pairingInfo tells the pairing page which way this browser can pair.
type pairingInfo struct {
	Shared   bool `json:"shared"`   // by invitation, or with a passkey
	Passkey  bool `json:"passkey"`  // this address can use passkeys
	Passkeys bool `json:"passkeys"` // one is registered, so one can sign in
}

// pairing answers GET /pair, before the browser is paired.
func (b *bridge) pairing(w http.ResponseWriter, r *http.Request) {
	info := pairingInfo{Shared: b.shared() && !b.readOnly, Passkey: b.shared() && !b.readOnly && passkeyHost(r.Host, b.secure(r.Host))}
	if info.Passkey {
		keys, _ := b.devices.Passkeys()
		info.Passkeys = len(keys) > 0
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

func (b *bridge) pair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != b.scheme(r.Host)+"://"+r.Host || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "Same-origin JSON POST required", http.StatusForbidden)
		return
	}
	var p struct {
		Code   string `json:"code"`
		Ticket string `json:"ticket"`
		Device string `json:"device"`
		Invite string `json:"invite"`
		// Host says another nodwyn is linking to this one (ADR 54), not a
		// browser.
		Host bool `json:"host"`
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
	case p.Invite != "":
		// Another machine pairs as a named device: its answers carry the
		// name, and it can be revoked without restarting anything.
		if !b.shared() || b.readOnly {
			http.Error(w, "Device pairing is not enabled", http.StatusForbidden)
			return
		}
		if p.Device == "" {
			http.Error(w, "Name this device, for example phone", http.StatusBadRequest)
			return
		}
		join := func() (string, error) { return b.devices.Join(p.Invite, p.Device) }
		if p.Host {
			// A link is the person's only where a browser could be: where
			// what the person types does not cross the network in clear.
			join = func() (string, error) {
				return b.devices.Link(p.Invite, p.Device, passkeyHost(r.Host, b.secure(r.Host)))
			}
		}
		token, err := join()
		if errors.Is(err, errInvite) {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b.signIn(w, r, token)
		if p.Host {
			b.logf("%s linked to this nodwyn with an invitation", p.Device)
		} else {
			b.logf("%s paired with an invitation", p.Device)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	case b.shared():
		http.Error(w, "This console pairs other devices with an invitation: run nodwyn devices invite on the computer nodwyn runs on, or invite one from a device already paired", http.StatusForbidden)
		return
	case subtle.ConstantTimeCompare([]byte(p.Code), []byte(b.code)) != 1:
		http.Error(w, "Invalid pairing code", http.StatusForbidden)
		return
	case p.Device != "":
		http.Error(w, "Device pairing is not enabled", http.StatusForbidden)
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
	case "schedule.read", "schedule.next", "workflow.list", "workflow.read", "outbox.list", "hello", "session.list", "session.watch", "session.info", "session.totals", "session.subscribe", "session.unsubscribe", "log.read", "log.runs", "conversation.search", "log.context",
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

// pingEvery is how often the bridge pings an open console, which has twice
// that to answer: longer than a relayed call holds up the reading of its
// answer. A proxy in front of the console (tailscale serve) or a network on
// the way may close a connection that carries nothing for a while, and a
// browser that slept or lost its network leaves one open with nobody at the
// other end. The pings keep the first open and end the second, with the
// daemon connection it holds.
const pingEvery = 30 * time.Second

// relay is what the bridge tells the daemon of a device it relays for: its
// name as via, and the passkey its person proved themself with, if any.
func relay(device Device) (via, passkey string) {
	if device.Name == "" {
		return "", ""
	}
	return "web:" + device.Name, device.Passkey
}

func (b *bridge) connect(w http.ResponseWriter, r *http.Request, device Device, token string) {
	ctx, cancel := context.WithCancel(b.ctx)
	defer cancel()
	// A device token is another machine's authority wherever it arrives
	// from: only this machine's pairing grants local powers.
	remote := b.remoteFor(r, device.Name)
	via, passkey := relay(device)
	dialCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	client, err := rpc.DialAs(dialCtx, b.socket, via, passkey)
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
	paired := func() (Device, bool) { return b.stillPaired(device, token) }
	// Who the requests come from now: a passkey removed meanwhile makes the
	// device one that only answers at once, though its daemon connection
	// still carries the passkey; one that touches a passkey reconnects.
	who := func(now Device) caller {
		return caller{local: !remote, person: now.Passkey != "" && now.Passkey == device.Passkey, touched: now.Name != "" && b.touched(now.Hash), link: now.Host}
	}
	revoked := func() {
		conn.Close(websocket.StatusPolicyViolation, "this device was revoked")
		cancel()
	}
	b.keepAlive(ctx, conn, cancel)
	b.watchDevice(ctx, device, token, revoked)
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
		current, ok := paired()
		if !ok {
			revoked()
			return
		}
		c := who(current)
		access := b.access(req.Method, c)
		// A setup action a device takes renews its touch, and is said in
		// the audit (Options.Audit), whether it worked or not.
		audited := access == granted && current.Name != "" && b.tierOf(req.Method) >= tierSetup
		if audited {
			b.touch(current.Hash)
		}
		var result json.RawMessage
		var callErr error
		switch {
		case req.JSONRPC != "2.0" || req.ID == 0:
			callErr = errors.New("invalid request")
		case len(req.Params) >= maxRequest:
			callErr = errors.New("that message is too large to send: a request is at most 1 MB")
		case access == needsTouch:
			callErr = errTouch
		case access == refused:
			callErr = errors.New("operation unavailable in this browser mode; use the terminal client")
			if b.readOnly {
				callErr = errors.New("this browser view is read-only; use the terminal client for this operation")
			} else if remote {
				callErr = errors.New("this is not available from this device: sign in with your passkey, or use nodwyn on its own computer")
			}
		case strings.HasPrefix(req.Method, "devices.") || strings.HasPrefix(req.Method, "passkeys."):
			result, callErr = b.deviceMethod(req, current, c)
		case slowCalls[req.Method] > 0:
			// Answered when it is done; the console's next requests are
			// read meanwhile.
			go func(req request) {
				result, err := b.slowCall(ctx, via, passkey, req)
				if audited {
					b.audited(current, req.Method, err)
				}
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
					// A setup method waiting for a touch is offered: the
					// console asks for the passkey when it is used.
					methods := []string{}
					for _, method := range append(hello.Methods, "devices.list", "devices.revoke", "devices.invite", "devices.leave", "passkeys.list", "passkeys.remove") {
						if b.access(method, c) != refused {
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
		if audited {
			b.audited(current, req.Method, callErr)
		}
		if err := write(ctx, conn, reply(req.ID, result, callErr)); err != nil {
			return
		}
	}
}

// stillPaired reports whether device, the zero Device for this computer's
// browser, is still paired with token, and what it is now.
func (b *bridge) stillPaired(device Device, token string) (Device, bool) {
	if device.Name == "" {
		return device, true
	}
	now, ok := b.devices.Check(token)
	return now, ok && now.Name == device.Name
}

// keepAlive pings an open console every pingEvery (b.ping) and ends it, with
// cancel, when one goes unanswered. The pong is read by whatever reads the
// console's messages.
func (b *bridge) keepAlive(ctx context.Context, conn *websocket.Conn, cancel func()) {
	every := b.ping
	if every <= 0 {
		every = pingEvery
	}
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				pingCtx, stop := context.WithTimeout(ctx, 2*every)
				err := conn.Ping(pingCtx)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
}

// watchDevice checks every deviceRecheck (b.recheck) that a paired device's
// console is still paired, and calls revoked once it is not, so a revocation
// also ends a console that is only watching.
func (b *bridge) watchDevice(ctx context.Context, device Device, token string, revoked func()) {
	if device.Name == "" {
		return
	}
	every := b.recheck
	if every <= 0 {
		every = deviceRecheck
	}
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if _, ok := b.stillPaired(device, token); !ok {
					revoked()
					return
				}
			}
		}
	}()
}

// audited says in the audit that device took a setup action.
func (b *bridge) audited(device Device, method string, err error) {
	outcome := "done"
	if err != nil {
		outcome = "failed: " + err.Error()
	}
	b.logf("%s by %s with the passkey %s: %s", method, device.Name, device.Passkey, outcome)
}

// reply is a relayed call's answer.
func reply(id uint64, result json.RawMessage, err error) map[string]any {
	r := map[string]any{"jsonrpc": "2.0", "id": id}
	if errors.Is(err, errTouch) {
		r["error"] = map[string]any{"code": touchCode, "message": err.Error()}
	} else if err != nil {
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
	// A workflow's source is a forge's answer or a command's, which may take
	// up to a minute each.
	"workflow.test":  65 * time.Second,
	"workflow.check": 65 * time.Second,
}

// slowCall relays one of slowCalls over a daemon connection of its own.
func (b *bridge) slowCall(ctx context.Context, via, passkey string, req request) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, slowCalls[req.Method])
	defer cancel()
	client, err := rpc.DialAs(ctx, b.socket, via, passkey)
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

// A caller is who a console's requests come from.
type caller struct {
	local   bool // this computer's browser (ADR 30)
	person  bool // a device signed in with a passkey still registered
	touched bool // such a device whose person touched it within touchLife
	link    bool // another nodwyn linked to this one, which never touches (ADR 54)
}

// tier is who may call a method, from the widest.
type tier int

const (
	tierNone    tier = iota // no browser
	tierRead                // every console, a read-only one too
	tierAnswer              // every paired device: answers and steers
	tierPerson              // the person's device: this computer's choices
	tierSetup               // the person's device after a touch: setup and secrets
	tierPasskey             // the same, and no one else: not this computer's cookie
)

// tierOf places a method. Only operations with a complete browser view join
// this list; core remains authoritative for every answer and its live
// prompt ID. A device paired by invitation answers and steers, but never
// widens policy or changes nodwyn's setup (ADR 30). A device signed in with
// a passkey is the person's, and makes the choices this computer makes;
// what changes setup or touches a secret asks it for a touch of the passkey
// first, once per sitting (amended 2026-10-17).
func (b *bridge) tierOf(method string) tier {
	if readOnly(method) {
		return tierRead
	}
	switch method {
	case "session.open", "worktree.open", "worktree.review", "session.resume", "session.send", "session.abort", "session.close", "session.answer", "session.approval.create", "session.friction", "session.policy.read",
		"schedule.pause", "schedule.run", "workflow.pause", "workflow.check", "outbox.decide", "session.notes.write", "session.restore", "session.rename", "session.archive":
		return tierAnswer
	// "Don't ask" can be turned off from a device, the safe direction; the
	// daemon refuses to turn it on for one (ADR 30), so a device can stop a
	// task that is skipping its questions without aborting it.
	case "session.approve_all":
		return tierAnswer
	// Choosing a conversation's model, or the judge its plans and commands
	// go to, decides where its content goes next (ADR 40, ADR 47); lifting a
	// task's turn limits and setting its schedule decide what runs with
	// nobody watching (ADR 49). A key the model asks for (session.secret)
	// and the keychain's passphrase are secrets the person types, and the
	// passphrase is its own proof. The folder last picked only remembers.
	case "session.model.set", "session.judge.set", "session.limits.set", "schedule.set", "session.secret", "keychain.unlock", "workspace.active", "startup.read", "trust.read":
		return tierPerson
	// A workflow decides what runs with nobody watching, as a schedule does,
	// and its source is asked with this computer's keys (ADR 53 §9).
	case "workflow.write", "workflow.remove", "workflow.test":
		return tierPerson
	// A linked host's link is this computer's authority there (ADR 54): the
	// person uses it, from this computer or their passkey's device, and a
	// device that only answers here does not. The console uses it through
	// its own socket and uploads for that host, never by calling
	// host.connect or host.attach itself.
	case "host.list":
		return tierPerson
	// Trusting a project lets its Scheme run as the person: it needs the
	// passkey, though this computer's own console may do the rest of setup,
	// since the cookie of that console goes to every server on loopback,
	// a task's dev server among them (ADR 30, amended 2026-10-16; ADR 45,
	// amended 2026-10-17). `nodwyn trust` is this computer's way.
	case "trust.set":
		return tierPasskey
	// A log is the record of what a session did, devices included (ADR 39).
	case "session.delete", "session.binding.create", "session.policy.write", "keychain.create", "keychain.passphrase", "settings.control.write", "settings.docs.write", "settings.write",
		"settings.provider.write", "settings.provider.remove", "settings.model.write", "settings.model.remove", "models.test",
		"workspace.save", "workspace.remove", "index.forget", "mcp.inspect", "mcp.save", "mcp.approve", "mcp.remove", "mcp.pull",
		"credentials.add", "credentials.replace", "credentials.remove", "credentials.bind", "credentials.unbind", "credentials.authorize", "credentials.authorize.finish",
		// What starts at this computer's login is its setup (ADR 30, ADR 53),
		// and so are the other nodwyns it is linked to (ADR 54).
		"startup.write", "host.link", "host.unlink":
		return tierSetup
	}
	if b.devices == nil {
		return tierNone
	}
	// The console's own methods (deviceMethod). A plain invitation lets in
	// a device that only answers, so a device may make one; one that may
	// register a passkey is a setup action (deviceMethod).
	switch method {
	// A device may end its own pairing: another nodwyn that unlinks asks it
	// (ADR 54).
	case "devices.invite", "devices.leave":
		return tierAnswer
	case "devices.list", "passkeys.list":
		return tierPerson
	case "devices.revoke", "passkeys.remove":
		return tierSetup
	}
	return tierNone
}

// access is what a caller may do with a method.
type access int

const (
	refused    access = iota
	needsTouch        // offered: the call waits for a touch of the passkey
	granted
)

func (b *bridge) access(method string, c caller) access {
	t := b.tierOf(method)
	switch {
	case t == tierRead:
		return granted
	case t == tierNone || b.readOnly:
		return refused
	case t == tierPasskey:
		switch {
		case !c.person:
			return refused
		case c.touched:
			return granted
		}
		return c.touch()
	case c.local || t == tierAnswer:
		return granted
	case !c.person:
		return refused
	case t == tierPerson || c.touched:
		return granted
	}
	return c.touch()
}

// touch is what a setup action waits for from the person's device: a touch
// of the passkey, which another nodwyn linked to this one cannot give, so it
// is not offered one (ADR 54).
func (c caller) touch() access {
	if c.link {
		return refused
	}
	return needsTouch
}

// allowed reports whether c may call method now.
func (b *bridge) allowed(method string, c caller) bool { return b.access(method, c) == granted }

// errTouch refuses a setup action from the person's device before its
// touch: the console asks for the passkey and sends the request again.
var errTouch = errors.New("confirm with your passkey to change nodwyn's setup from this device")

// touchCode is errTouch's JSON-RPC error code, which the console looks for.
const touchCode = -32030

// maxUpload bounds one attached file.
const maxUpload = 20 << 20

// attach receives one file for a session (POST /attach?id=SESSION&name=NAME,
// the file as the body). The bridge stages it in a private directory, asks
// the daemon to attach that path, and removes it: a page never names a path
// the daemon reads.
func (b *bridge) attach(w http.ResponseWriter, r *http.Request, device Device) {
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
	via, passkey := relay(device)
	// An image waits for its confirm, which the daemon gives up on after
	// ten minutes; the call waits a little longer than that, and one for a
	// linked host, which waits as long there, longer still.
	ctx, cancel := context.WithTimeout(b.ctx, 12*time.Minute)
	defer cancel()
	client, err := rpc.DialAs(ctx, b.socket, via, passkey)
	if err != nil {
		http.Error(w, "Daemon unavailable", http.StatusServiceUnavailable)
		return
	}
	defer client.Close()
	method, params := "session.attach", map[string]string{"id": id, "path": staged, "name": name}
	if host := r.URL.Query().Get("host"); host != "" {
		// A file for a task on a linked host goes there through this
		// computer's link (ADR 54), which is the person's to use.
		if !b.allowed("host.list", b.callerOf(r, device)) {
			http.Error(w, "Linked hosts are reached from this computer, or from a device signed in with your passkey", http.StatusForbidden)
			return
		}
		method, params = "host.attach", map[string]string{"name": host, "id": id, "path": staged, "file": name}
	}
	var receipt json.RawMessage
	if err := client.Call(ctx, method, params, &receipt); err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(receipt)
}

// deviceMethod answers the console's own methods, which the daemon does
// not know: the devices paired with this console, invitations, and the
// passkeys their person signs in with. device is the caller's, the zero
// Device for this computer.
func (b *bridge) deviceMethod(req request, device Device, c caller) (json.RawMessage, error) {
	var p struct {
		Name    string `json:"name"`
		Passkey bool   `json:"passkey"`
	}
	if len(req.Params) > 0 {
		decoder := json.NewDecoder(strings.NewReader(string(req.Params)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&p); err != nil {
			return nil, errors.New("invalid parameters")
		}
	}
	// Who an invitation is by: the device, or this computer's console.
	by := "computer"
	if device.Name != "" {
		by = device.Name
	}
	switch req.Method {
	case "devices.list":
		devices, err := b.devices.List()
		if devices == nil {
			devices = []Device{}
		}
		return marshal(devices, err)
	case "passkeys.list":
		keys, err := b.devices.Passkeys()
		if keys == nil {
			keys = []Passkey{}
		}
		return marshal(keys, err)
	case "devices.invite":
		// An invitation that may register a passkey makes a device the
		// person's, so it is a setup action; a plain one lets in a device
		// that only answers, as the one inviting does.
		if p.Passkey && !c.local {
			if !c.person {
				return nil, errors.New("only a device signed in with a passkey invites one that may register a passkey")
			}
			if !c.touched {
				return nil, errTouch
			}
			b.touch(device.Hash)
		}
		invite := func() (string, time.Time, error) { return b.devices.Invite(by, p.Passkey) }
		if p.Passkey && !c.local {
			// The person's device vouches with its passkey: another nodwyn
			// that uses the invitation is theirs (ADR 54).
			invite = func() (string, time.Time, error) { return b.devices.InviteAsPerson(by, device.Passkey) }
		}
		code, expires, err := invite()
		if err != nil {
			return nil, err
		}
		b.logf("%s made an invitation (passkey: %v)", by, p.Passkey)
		return json.Marshal(map[string]any{"code": code, "expires": expires})
	case "devices.leave":
		if device.Name == "" {
			return nil, errors.New("this computer is not a paired device")
		}
		if err := b.devices.Revoke(device.Name); err != nil {
			return nil, err
		}
		b.logf("%s left", device.Name)
		return json.Marshal(struct{}{})
	}
	if p.Name == "" {
		return nil, errors.New("name is required")
	}
	var err error
	if req.Method == "devices.revoke" {
		err = b.devices.Revoke(p.Name)
	} else {
		err = b.devices.RemovePasskey(p.Name)
	}
	return marshal(struct{}{}, err)
}

// marshal is v as JSON, or err.
func marshal(v any, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
