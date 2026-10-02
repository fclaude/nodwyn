// Package egress is the proxy a contained MCP server reaches the network
// through (ADR 23 W3b). It runs in a sidecar container, the only one on the
// server's internal network with a route out, and lets through exactly the
// hosts the person listed: a CONNECT tunnel or a plain-HTTP request to a
// listed host and port, nothing else. It is not an open proxy and not a web
// server. Every attempt, allowed or not, is reported as one JSON line, which
// nodwyn records in the session log.
//
// It imports nothing of nodwyn's: cmd/nodwyn-egress is built static, for
// Linux, and runs inside someone else's image.
package egress

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Report is one line of the proxy's output: first where it listens, then
// one attempt per line. It names a host and a port, never a path or a
// query, which can carry what the server is sending.
type Report struct {
	Listening string `json:"listening,omitempty"`
	Method    string `json:"method,omitempty"` // CONNECT, or a plain-HTTP request's method
	Host      string `json:"host,omitempty"`
	Port      int    `json:"port,omitempty"`
	// Allowed reports whether the proxy made the connection: false when
	// the host is not on the list, resolves to a local address, or could
	// not be reached; Reason says which.
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// Allow parses the allowlist. An entry is a host name or address, which
// allows ports 443 and 80, or host:port, which allows that port alone. An
// IPv6 address with a port is written [addr]:port. Matching is exact: no
// wildcard, no subdomain.
func Allow(entries []string) (map[string]bool, error) {
	allow := map[string]bool{}
	for _, entry := range entries {
		host, port, err := SplitEntry(entry)
		if err != nil {
			return nil, err
		}
		if port != 0 {
			allow[net.JoinHostPort(host, strconv.Itoa(port))] = true
			continue
		}
		allow[net.JoinHostPort(host, "443")] = true
		allow[net.JoinHostPort(host, "80")] = true
	}
	return allow, nil
}

// SplitEntry splits an allowlist entry into a lower-case host and a port,
// 0 when the entry names none.
func SplitEntry(entry string) (string, int, error) {
	bad := fmt.Errorf("egress: %q is not a host or host:port", entry)
	host, port := entry, 0
	if strings.HasPrefix(entry, "[") || strings.Count(entry, ":") == 1 {
		h, p, err := net.SplitHostPort(entry)
		if err != nil {
			return "", 0, bad
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", 0, bad
		}
		host, port = h, n
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if _, err := netip.ParseAddr(host); err == nil {
		return host, port, nil
	}
	if !hostname(host) {
		return "", 0, bad
	}
	return host, port, nil
}

// hostname reports whether s is a DNS name: dot-separated labels of
// letters, digits and dashes, with no wildcard.
func hostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// Proxy is the allowlisting proxy, an http.Handler.
type Proxy struct {
	allow  map[string]bool
	report func(Report)
	// Dial connects to an allowed host and port; tests replace it.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// New makes a proxy for the allowlist entries. report receives every
// attempt; it must be safe to call concurrently.
func New(entries []string, report func(Report)) (*Proxy, error) {
	allow, err := Allow(entries)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: refuseLocal}
	return &Proxy{allow: allow, report: report, Dial: dialer.DialContext}, nil
}

// refuseLocal keeps a listed name that resolves to this machine or to a
// link-local address (a cloud metadata service, a rootless network's path
// to the host's loopback) from being reached through the proxy.
func refuseLocal(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	ip := ap.Addr().Unmap()
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("egress: %s is a local address", ip)
	}
	return nil
}

// allowed reports whether host:port is on the list.
func (p *Proxy) allowed(host string, port int) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return p.allow[net.JoinHostPort(host, strconv.Itoa(port))]
}

func (p *Proxy) emit(r Report) {
	if p.report != nil {
		p.report(r)
	}
}

// ServeHTTP handles one proxy request.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	// A plain-HTTP proxy request names an absolute http URL. Anything else
	// (a request for the proxy's own pages, an https URL it would have to
	// fetch itself) is refused.
	if !r.URL.IsAbs() || r.URL.Scheme != "http" || r.URL.Host == "" {
		p.emit(Report{Method: r.Method, Host: r.URL.Hostname(), Allowed: false, Reason: "not a proxy request for an http URL"})
		http.Error(w, "nodwyn-egress: only CONNECT and absolute http URLs", http.StatusBadRequest)
		return
	}
	host, port := r.URL.Hostname(), 80
	if s := r.URL.Port(); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			http.Error(w, "nodwyn-egress: bad port", http.StatusBadRequest)
			return
		}
		port = n
	}
	if !p.allowed(host, port) {
		p.emit(Report{Method: r.Method, Host: strings.ToLower(host), Port: port, Allowed: false, Reason: "not on the server's network list"})
		http.Error(w, "nodwyn-egress: "+host+" is not on this server's network list", http.StatusForbidden)
		return
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	upstream, err := p.Dial(r.Context(), "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		p.emit(Report{Method: r.Method, Host: host, Port: port, Allowed: false, Reason: err.Error()})
		http.Error(w, "nodwyn-egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	p.emit(Report{Method: r.Method, Host: host, Port: port, Allowed: true})
	// One request over the connection just checked and made: the Host
	// header follows the address checked, not the client.
	conn := make(chan net.Conn, 1)
	conn <- upstream
	forward := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = pr.In.URL
			pr.Out.Host = pr.In.URL.Host
		},
		Transport: &http.Transport{
			Proxy:             nil,
			DialContext:       func(context.Context, string, string) (net.Conn, error) { return once(conn) },
			DisableKeepAlives: true,
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "nodwyn-egress: "+err.Error(), http.StatusBadGateway)
		},
	}
	forward.ServeHTTP(w, r)
	select {
	case unused := <-conn:
		unused.Close()
	default:
	}
}

// once hands out the one connection and fails after.
func once(conn chan net.Conn) (net.Conn, error) {
	select {
	case c := <-conn:
		return c, nil
	default:
		return nil, errors.New("connection already used")
	}
}

// connect opens a tunnel to an allowed host. What goes through it is the
// server's own business (TLS, usually); where it goes is not.
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, portText, err := net.SplitHostPort(r.Host)
	port, perr := strconv.Atoi(portText)
	if err != nil || perr != nil {
		p.emit(Report{Method: "CONNECT", Host: strings.ToLower(r.Host), Allowed: false, Reason: "not host:port"})
		http.Error(w, "nodwyn-egress: CONNECT needs host:port", http.StatusBadRequest)
		return
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !p.allowed(host, port) {
		p.emit(Report{Method: "CONNECT", Host: host, Port: port, Allowed: false, Reason: "not on the server's network list"})
		http.Error(w, "nodwyn-egress: "+host+" is not on this server's network list", http.StatusForbidden)
		return
	}
	upstream, err := p.Dial(r.Context(), "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		p.emit(Report{Method: "CONNECT", Host: host, Port: port, Allowed: false, Reason: err.Error()})
		http.Error(w, "nodwyn-egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "nodwyn-egress: cannot tunnel", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	p.emit(Report{Method: "CONNECT", Host: host, Port: port, Allowed: true})
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	tunnel(client, buffered, upstream)
}

// tunnel copies both ways until either side closes. Bytes the client sent
// after its CONNECT, already read into buffered, go first.
func tunnel(client net.Conn, buffered *bufio.ReadWriter, upstream net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if n := buffered.Reader.Buffered(); n > 0 {
			early, _ := buffered.Reader.Peek(n)
			if _, err := upstream.Write(early); err != nil {
				upstream.Close()
				return
			}
		}
		_, _ = io.Copy(upstream, client)
		closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		closeWrite(client)
	}()
	wg.Wait()
	client.Close()
	upstream.Close()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// Serve accepts proxy connections on l until ctx ends. It reports where it
// listens first, so whoever started it knows it is ready.
func (p *Proxy) Serve(ctx context.Context, l net.Listener) error {
	server := &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second, ErrorLog: nil}
	go func() {
		<-ctx.Done()
		server.Close()
	}()
	p.emit(Report{Listening: l.Addr().String()})
	err := server.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Lines returns a report function that writes each report as one JSON
// line to w, one at a time.
func Lines(w io.Writer) func(Report) {
	var mu sync.Mutex
	return func(r Report) {
		var line []byte
		if r.Listening != "" {
			line, _ = json.Marshal(map[string]string{"listening": r.Listening})
		} else {
			line, _ = json.Marshal(r)
		}
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write(append(line, '\n'))
	}
}
