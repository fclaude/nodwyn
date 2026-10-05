//go:build webview

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// This runs the real Wails event loop and JavaScript bridge, not a browser mock.
// Opt in on a machine with a desktop session; ordinary acceptance stays headless.
func TestWailsWindowRoundTrip(t *testing.T) {
	if os.Getenv("NODWYN_TEST_DESKTOP") != "1" {
		t.Skip("set NODWYN_TEST_DESKTOP=1 in a desktop session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWailsWindowRoundTrip$")
	child.Env = append(os.Environ(), "NODWYN_TEST_DESKTOP_HELPER=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("desktop helper: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
}

func init() { desktopTestMain = runDesktopCheck }

// Wails must run on the process main thread, which TestMain retains.
func runDesktopCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/result" {
			result <- r.URL.Query().Get("value")
			cancel()
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Nodwyn desktop check</title><p>Testing native bridge</p><script>
window.addEventListener('nodwyn-desktop-ready', async () => {
  let result = 'unexpected success';
  try { await window.nodwynOpenExternal('file:///not-a-web-link'); }
  catch (error) { result = error.message; }
  await fetch('/result?value=' + encodeURIComponent(result));
});
</script>`)
	}))
	defer server.Close()
	if err := openWindow(ctx, server.URL+"/", false); err != nil {
		panic(err)
	}
	select {
	case got := <-result:
		if !strings.Contains(got, "only web links") {
			panic("native reply: " + got)
		}
	default:
		panic("native window did not complete its JavaScript-to-Go round trip")
	}
}
