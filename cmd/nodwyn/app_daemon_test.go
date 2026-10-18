package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
)

// A daemon that listens but cannot be used (an older protocol, a hello it
// refuses) is still running its tasks: the app leaves its socket and says
// so. It used to remove the socket and start a second daemon beside it.
func TestAppLeavesADaemonItCannotUse(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "nodwyn-app-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "sock")
	other, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	go func() {
		for {
			conn, err := other.Accept()
			if err != nil {
				return
			}
			conn.Write([]byte("not nodwyn\n"))
			conn.Close()
		}
	}()
	paths := config.PathsUnder(filepath.Join(dir, "config"), filepath.Join(dir, "data"))
	_, _, err = ensureDaemon(context.Background(), paths, socket, "app")
	if err == nil || !strings.Contains(err.Error(), "cannot use it") {
		t.Fatalf("ensureDaemon: %v", err)
	}
	if _, err := os.Lstat(socket); err != nil {
		t.Fatalf("the other daemon's socket went: %v", err)
	}
}
