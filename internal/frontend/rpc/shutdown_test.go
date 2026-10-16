package rpc

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// A method waiting on a slow provider does not keep the daemon from closing
// its sessions when it is told to stop (P2-076): the wait ends with Serve's
// context instead of running out its own, which can be minutes.
func TestShutdownDoesNotWaitForASlowProvider(t *testing.T) {
	asked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hang := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		once.Do(func() { close(asked) })
		<-release
	}))
	defer hang.Close()
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	o := options(t)
	o.Config.Control.URL, o.Config.Control.Model = hang.URL, "slow"
	path, stop := start(t, o)
	c := connect(t, path)
	c.hello()
	// The reply is never read: the connection closes with the daemon.
	if err := c.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "models.test", "params": map[string]any{"name": "control"}}); err != nil {
		t.Fatal(err)
	}
	<-asked
	began := time.Now()
	stop()
	if took := time.Since(began); took > 3*time.Second {
		t.Fatalf("Serve returned %v after it was told to stop", took)
	}
}
