package egress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// rig is a proxy whose dialer reaches a local test server whatever host
// it is asked for, so an allowed host needs no network; what was dialled
// and reported is kept.
type rig struct {
	proxy   *Proxy
	addr    string
	mu      sync.Mutex
	dialed  []string
	reports []Report
}

func newRig(t *testing.T, allow ...string) *rig {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "upstream saw %s %s host=%s", r.Method, r.URL.Path, r.Host)
	}))
	t.Cleanup(upstream.Close)
	r := &rig{}
	p, err := New(allow, func(rep Report) {
		r.mu.Lock()
		r.reports = append(r.reports, rep)
		r.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	p.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		r.mu.Lock()
		r.dialed = append(r.dialed, address)
		r.mu.Unlock()
		var d net.Dialer
		return d.DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	r.proxy = p
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Serve(ctx, l)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	r.addr = l.Addr().String()
	return r
}

func (r *rig) seen() ([]string, []Report) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.dialed...), append([]Report(nil), r.reports...)
}

// connect sends CONNECT target and, when the tunnel opens, a GET through it.
func (r *rig) connect(t *testing.T, target string) (int, string) {
	t.Helper()
	conn, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, ""
	}
	fmt.Fprintf(conn, "GET /through HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target)
	inner, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(inner.Body)
	return resp.StatusCode, string(body)
}

func TestConnectToAListedHostTunnels(t *testing.T) {
	r := newRig(t, "api.example.com", "uploads.example.com:8443")
	code, body := r.connect(t, "api.example.com:443")
	if code != http.StatusOK || !strings.Contains(body, "upstream saw GET /through") {
		t.Fatalf("tunnel: %d %q", code, body)
	}
	code, _ = r.connect(t, "uploads.example.com:8443")
	if code != http.StatusOK {
		t.Fatalf("listed port: %d", code)
	}
	dialed, reports := r.seen()
	if strings.Join(dialed, ",") != "api.example.com:443,uploads.example.com:8443" {
		t.Fatalf("dialed %v", dialed)
	}
	if len(reports) != 3 || reports[0].Listening == "" || reports[1] != (Report{Method: "CONNECT", Host: "api.example.com", Port: 443, Allowed: true}) {
		t.Fatalf("reports %+v", reports)
	}
}

// Anything not on the list is refused before a connection is made: another
// host, a listed host on another port, the proxy itself, an address.
func TestEverythingElseIsRefused(t *testing.T) {
	r := newRig(t, "api.example.com", "uploads.example.com:8443")
	for _, target := range []string{"evil.example.net:443", "api.example.com:22", "uploads.example.com:443", "sub.api.example.com:443", r.addr, "127.0.0.1:443", "API.EXAMPLE.COM.evil:443"} {
		if code, _ := r.connect(t, target); code != http.StatusForbidden {
			t.Errorf("CONNECT %s: %d", target, code)
		}
	}
	dialed, reports := r.seen()
	if len(dialed) != 0 {
		t.Fatalf("dialed %v", dialed)
	}
	for _, rep := range reports[1:] {
		if rep.Allowed || rep.Reason == "" || rep.Method != "CONNECT" {
			t.Fatalf("report %+v", rep)
		}
	}
	// A host is matched exactly, in any case, and with a trailing dot.
	if code, _ := r.connect(t, "API.Example.COM.:443"); code != http.StatusOK {
		t.Fatalf("case and trailing dot: %d", code)
	}
}

// Plain HTTP through the proxy reaches a listed host on port 80 (or the
// listed port), with the Host header of the address checked; it is not an
// open proxy and not a web server.
func TestPlainHTTPIsForwardedOnlyToListedHosts(t *testing.T) {
	r := newRig(t, "api.example.com")
	proxyURL, _ := url.Parse("http://" + r.addr)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	// The request names one host and its Host header another: the one
	// checked is the one reached and the one the upstream sees.
	conn, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(conn, "GET http://api.example.com/v1/items?token=secret HTTP/1.1\r\nHost: evil.example.net\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	conn.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "host=api.example.com") {
		t.Fatalf("forwarded: %d %s", resp.StatusCode, body)
	}
	for _, target := range []string{"http://evil.example.net/", "http://api.example.com:8080/"} {
		resp, err := client.Get(target)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: %d", target, resp.StatusCode)
		}
	}
	// Asked as a web server, or to fetch an https URL itself, it refuses.
	for _, raw := range []string{"GET / HTTP/1.1\r\nHost: " + r.addr + "\r\n\r\n", "GET https://api.example.com/ HTTP/1.1\r\nHost: api.example.com\r\n\r\n"} {
		conn, err := net.Dial("tcp", r.addr)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(conn, raw)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		conn.Close()
		if err != nil || resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%q: %v %v", raw, resp, err)
		}
	}
	dialed, reports := r.seen()
	if strings.Join(dialed, ",") != "api.example.com:80" {
		t.Fatalf("dialed %v", dialed)
	}
	for _, rep := range reports {
		if strings.Contains(rep.Host, "token") || strings.Contains(rep.Reason, "secret") {
			t.Fatalf("a report carries more than host and port: %+v", rep)
		}
	}
}

// The real dialer refuses a listed name that resolves to this machine or a
// link-local address.
func TestLocalAddressesAreNotReached(t *testing.T) {
	var reports []Report
	p, err := New([]string{"localhost:9", "127.0.0.1:9"}, func(r Report) { reports = append(reports, r) })
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"localhost:9", "127.0.0.1:9"} {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, &http.Request{Method: http.MethodConnect, Host: target, URL: &url.URL{Host: target}, Header: http.Header{}})
		if w.Code != http.StatusBadGateway {
			t.Fatalf("%s: %d", target, w.Code)
		}
	}
	if len(reports) != 2 || reports[0].Allowed || !strings.Contains(reports[0].Reason, "local address") {
		t.Fatalf("reports %+v", reports)
	}
	for _, addr := range []string{"169.254.169.254:80", "[fe80::1]:443", "0.0.0.0:443", "[::1]:443"} {
		if refuseLocal("tcp", addr, nil) == nil {
			t.Errorf("%s was not refused", addr)
		}
	}
	if err := refuseLocal("tcp", "93.184.216.34:443", nil); err != nil {
		t.Fatal(err)
	}
}

func TestAllowlistEntries(t *testing.T) {
	allow, err := Allow([]string{"API.example.com", "uploads.example.com:8443", "10.0.0.5", "[2001:db8::1]:443"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"api.example.com:443", "api.example.com:80", "uploads.example.com:8443", "10.0.0.5:443", "[2001:db8::1]:443"} {
		if !allow[want] {
			t.Errorf("%s not allowed", want)
		}
	}
	if allow["uploads.example.com:443"] || len(allow) != 6 {
		t.Fatalf("allow %v", allow)
	}
	for _, bad := range []string{"*.example.com", "example.com:0", "example.com:99999", "http://example.com", "exa mple.com", "", "example.com/path", "-bad.com", "a..b"} {
		if _, err := Allow([]string{bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestLinesAreJSON(t *testing.T) {
	var b bytes.Buffer
	report := Lines(&b)
	report(Report{Listening: "[::]:3128"})
	report(Report{Method: "CONNECT", Host: "x.example", Port: 443})
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if lines[0] != `{"listening":"[::]:3128"}` {
		t.Fatalf("first line %s", lines[0])
	}
	var r Report
	if err := json.Unmarshal([]byte(lines[1]), &r); err != nil || r.Host != "x.example" || r.Allowed {
		t.Fatalf("%s: %+v %v", lines[1], r, err)
	}
}
