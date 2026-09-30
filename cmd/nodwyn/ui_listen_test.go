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
