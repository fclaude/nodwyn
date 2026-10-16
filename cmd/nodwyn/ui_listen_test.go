package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	webui "github.com/fclaude/nodwyn/internal/frontend/web"
)

// A wildcard --listen is refused without --name: browsers never send
// 0.0.0.0 as the host, so the console would refuse every request.
func TestWildcardListenNeedsAName(t *testing.T) {
	paths := config.PathsUnder(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data"))
	for _, address := range []string{"0.0.0.0:0", "[::]:0", ":0"} {
		r := &remoteFlags{listen: address}
		if _, _, err := r.listener(paths, webui.Options{}); err == nil || !strings.Contains(err.Error(), "--name") {
			t.Fatalf("%s: %v", address, err)
		}
	}
	r := &remoteFlags{listen: "0.0.0.0:0", hosts: []string{"desk.local:7777"}}
	l, _, err := r.listener(paths, webui.Options{PairingCode: strings.Repeat("x", 40)})
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}

// The console listens on the port it had the last time, so a browser finds
// what it remembered for that origin; a port taken since gives a new one,
// kept for next time.
func TestConsoleKeepsItsPort(t *testing.T) {
	paths := config.PathsUnder(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data"))
	first, err := loopbackListener(paths)
	if err != nil {
		t.Fatal(err)
	}
	port := first.Addr().String()
	first.Close()
	again, err := loopbackListener(paths)
	if err != nil {
		t.Fatal(err)
	}
	if again.Addr().String() != port {
		t.Fatalf("listening on %s, not on %s as last time", again.Addr(), port)
	}
	// Taken now (by the listener still open): another port, kept.
	other, err := loopbackListener(paths)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	again.Close()
	if other.Addr().String() == port {
		t.Fatal("two listeners on one port")
	}
	last, err := loopbackListener(paths)
	if err != nil {
		t.Fatal(err)
	}
	defer last.Close()
	if last.Addr().String() == other.Addr().String() {
		t.Fatal("a port still in use was reused")
	}
}
