package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
	"github.com/fclaude/rauli/browser"
)

// needBrowser skips a test that drives a real Chromium where there is none.
func needBrowser(t *testing.T) {
	t.Helper()
	if _, err := browser.Find(); err != nil {
		t.Skip(err)
	}
}

// counted is a server that counts the requests it is sent, which a test of a
// site off the list expects to stay at none.
func counted(t *testing.T, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// browserEgress are the browser.egress records in the log.
func (r *rig) browserEgress() []log.BrowserEgress {
	r.t.Helper()
	var out []log.BrowserEgress
	for _, ev := range r.logged() {
		var p log.BrowserEgress
		if ev.Kind == log.KindBrowserEgress && ev.Decode(&p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// The browser reaches the sites config.toml lists and nothing else: an
// address off the list is refused before it is sent, an image a listed page
// asks of another host is never fetched, and a WebSocket the page opens to
// a host off the list, which the request filter does not see, is refused by
// the proxy. A click asks, naming the page and the element; what the page
// says carries external:web.
func TestBrowserReachesOnlyItsSites(t *testing.T) {
	t.Parallel()
	needBrowser(t)
	off, offRequests := counted(t, `<!doctype html><title>Off</title><h1>Off the list</h1>`)
	// A plain listener: a WebSocket handshake that got through would be a
	// connection here, whatever came after it.
	ws, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	var wsConns atomic.Int64
	go func() {
		for {
			c, err := ws.Accept()
			if err != nil {
				return
			}
			wsConns.Add(1)
			c.Close()
		}
	}()
	page := fmt.Sprintf(`<!doctype html><title>Shop</title><h1>Shop</h1>
<img src="%s/pixel.png" alt="pixel">
<button onclick="document.title='paid'">Pay now</button>
<script>
const ws = new WebSocket("ws://%s/");
ws.onerror = () => { document.body.dataset.ws = "refused" };
</script>`, off.URL, ws.Addr().String())
	shop, _ := counted(t, page)
	shopHost := strings.TrimPrefix(shop.URL, "http://")

	r := newRigCalls(t, false, 10, func(c *config.Config) { c.Browser.Sites = []string{shopHost} },
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "go", Name: "browser-navigate", Args: map[string]any{"url": shop.URL}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "off", Name: "browser-navigate", Args: map[string]any{"url": off.URL}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "look", Name: "browser-snapshot", Args: map[string]any{}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "pay", Name: "browser-click", Args: map[string]any{"ref": "e1"}}}},
		fake.Turn{Content: "paid"},
	)
	r.s.Send("buy it")
	evs := r.drain("y")

	results := r.results()
	if results["go"] != "" || results["look"] != "" || results["pay"] != "" {
		t.Fatalf("a call on the listed site failed: %v", results)
	}
	if !strings.Contains(results["off"], "is not on the sites the browser may reach") {
		t.Fatalf("navigating off the list: %q, want the refusal", results["off"])
	}
	var confirms []event.Confirm
	for _, e := range evs {
		if e.Kind == event.PromptConfirm {
			confirms = append(confirms, e.Payload.(event.Confirm))
		}
	}
	if len(confirms) != 1 || confirms[0].Prompt.Prim != "browser-click" {
		t.Fatalf("confirms: %+v, want one, for the click", confirms)
	}
	if d := confirms[0].Prompt.Detail; !strings.Contains(d, "page: "+shop.URL) || !strings.Contains(d, `element: button "Pay now"`) {
		t.Fatalf("the click's confirm does not name the page and the button:\n%s", d)
	}

	// The WebSocket is refused by the proxy, and recorded, a moment after
	// the page loads.
	offHost := strings.TrimPrefix(off.URL, "http://")
	deadline := time.Now().Add(10 * time.Second)
	var refusedWS, refusedImage bool
	for time.Now().Before(deadline) && !(refusedWS && refusedImage) {
		refusedWS, refusedImage = false, false
		for _, e := range r.browserEgress() {
			at := net.JoinHostPort(e.Host, fmt.Sprint(e.Port))
			if !e.Allowed && e.Proxy && at == ws.Addr().String() {
				refusedWS = true
			}
			if !e.Allowed && !e.Proxy && at == offHost {
				refusedImage = true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !refusedWS || !refusedImage {
		t.Fatalf("refusals recorded: WebSocket %t, the other host %t; records: %+v", refusedWS, refusedImage, r.browserEgress())
	}
	for _, e := range r.browserEgress() {
		if !e.Allowed && !strings.Contains(e.Reason, "[browser] sites in config.toml") {
			t.Errorf("a refusal does not say which list: %+v", e)
		}
	}
	if n := offRequests.Load(); n != 0 {
		t.Fatalf("the host off the list was sent %d requests", n)
	}
	if n := wsConns.Load(); n != 0 {
		t.Fatalf("the WebSocket's host off the list got %d connections", n)
	}

	v, err := r.s.Eval(context.Background(), `(taint-of (browser-text))`)
	if err != nil {
		t.Fatal(err)
	}
	if got := scheme.Write(v); got != `("external:web")` {
		t.Fatalf("taint of a page: %s", got)
	}
}

// What is typed into a page is an egress payload: a literal key in it is
// refused before anything asks or the browser starts, since no binding names
// the listed site. With no sites the browser is not offered at all.
func TestBrowserTypingAKeyIsRefused(t *testing.T) {
	t.Parallel()
	const key = "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv"
	r := newRigWith(t, false, func(c *config.Config) { c.Browser.Sites = []string{"shop.example.com"} },
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "t", Name: "browser-type", Args: map[string]any{"ref": "e1", "text": "token " + key}}}},
		fake.Turn{Content: "refused"},
	)
	r.s.Send("log in")
	for _, e := range r.drain("y") {
		if e.Kind == event.PromptConfirm {
			t.Fatalf("typing a key asked: %+v", e.Payload)
		}
	}
	if d := r.mcpDecisions("browser-type"); len(d) != 1 || d[0].Decision != "deny" {
		t.Fatalf("decisions: %+v, want one denial", d)
	}
	if r.s.browser.session != nil {
		t.Fatal("a refused call started the browser")
	}

	plain := newRigWith(t, false, nil)
	for _, tool := range plain.s.Tools() {
		if strings.HasPrefix(tool.Name, "browser-") {
			t.Fatalf("a task with no [browser] sites was offered %s", tool.Name)
		}
	}
}

// The browser primitives' descriptions name no tool of Raulí's that a task
// is not offered, which the model would then call: type's advice to use
// fill_credential for a password is replaced.
func TestBrowserToolsNameOnlyOfferedTools(t *testing.T) {
	t.Parallel()
	r := newRigWith(t, true, func(c *config.Config) { c.Browser.Sites = []string{"docs.example.com"} })
	var offered int
	for _, p := range r.s.interp.Prims() {
		if !strings.HasPrefix(p.Name, "browser-") {
			continue
		}
		offered++
		for _, tool := range browser.Catalogue() {
			if _, ok := browserTools[tool.Name]; ok {
				continue
			}
			if strings.Contains(p.Meta.Doc, tool.Name) {
				t.Errorf("%s names %s, which is not offered: %s", p.Name, tool.Name, p.Meta.Doc)
			}
		}
	}
	if offered != len(browserTools) {
		t.Fatalf("%d browser primitives, want %d", offered, len(browserTools))
	}
}
