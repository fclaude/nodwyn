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
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/fclaude/nibb/internal/frontend/rpc"
)

//go:embed assets/*
var assets embed.FS

func PairingCode() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

type Options struct {
	Socket      string
	PairingCode string
	ReadOnly    bool
}

type bridge struct {
	readOnly           bool
	ctx                context.Context
	socket, host, code string
}

// Serve accepts only a 127.0.0.1 listener. The pairing code travels through the
// local terminal and a same-origin POST, never a URL, script-accessible storage or RPC log.
func Serve(ctx context.Context, listener net.Listener, options Options) error {
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) || len(options.PairingCode) < 32 {
		return fmt.Errorf("web: requires 127.0.0.1 and a strong pairing code")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b := &bridge{ctx: ctx, socket: options.Socket, host: listener.Addr().String(), code: options.PairingCode, readOnly: options.ReadOnly}
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

func (b *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self' ws://"+b.host+"; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if r.Host != b.host {
		http.Error(w, "Invalid host", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+b.host {
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
	case "/socket":
		if r.Method != http.MethodGet || r.Header.Get("Origin") != "http://"+b.host || !b.authenticated(r) {
			http.Error(w, "Pair this browser first", http.StatusForbidden)
			return
		}
		b.connect(w, r)
	case "/", "/app.js", "/prompts.js", "/inspectors.js", "/style.css":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		files, _ := fs.Sub(assets, "assets")
		http.FileServerFS(files).ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}
func (b *bridge) cookieName() string { return "nibb_ui_" + strings.ReplaceAll(b.host, ":", "_") }
func (b *bridge) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(b.cookieName())
	return err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(b.code)) == 1
}
func (b *bridge) pair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != "http://"+b.host || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "Same-origin JSON POST required", http.StatusForbidden)
		return
	}
	var p struct {
		Code string `json:"code"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil || subtle.ConstantTimeCompare([]byte(p.Code), []byte(b.code)) != 1 {
		http.Error(w, "Invalid pairing code", http.StatusForbidden)
		return
	}
	// HTTP is confined to loopback. HttpOnly keeps the capability out of script;
	// strict origin/host checks protect requests even from another loopback port.
	http.SetCookie(w, &http.Cookie{Name: b.cookieName(), Value: b.code, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func readOnly(method string) bool {
	switch method {
	case "hello", "session.list", "session.info", "session.subscribe", "session.unsubscribe", "log.read":
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

func (b *bridge) connect(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(b.ctx)
	defer cancel()
	dialCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	client, err := rpc.Dial(dialCtx, b.socket)
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
		var result json.RawMessage
		var callErr error
		switch {
		case req.JSONRPC != "2.0" || req.ID == 0:
			callErr = errors.New("invalid request")
		case !b.allowed(req.Method):
			callErr = errors.New("operation unavailable in this browser mode; use the terminal client")
			if b.readOnly {
				callErr = errors.New("this browser view is read-only; use the terminal client for this operation")
			}
		default:
			callCtx, stop := context.WithTimeout(ctx, 30*time.Second)
			if req.Method == "hello" {
				var hello rpc.HelloResult
				callErr = client.Call(callCtx, req.Method, req.Params, &hello)
				if callErr == nil {
					methods := []string{}
					for _, method := range hello.Methods {
						if b.allowed(method) {
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
func (b *bridge) allowed(method string) bool {
	if readOnly(method) {
		return true
	}
	if b.readOnly {
		return false
	}
	switch method {
	case "session.open", "session.resume", "session.send", "session.abort", "session.close", "session.answer", "session.secret", "session.approval.create", "session.binding.create", "session.friction":
		return true
	}
	return false
}
