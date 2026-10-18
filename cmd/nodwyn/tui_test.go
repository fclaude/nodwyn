package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/test/fakes/must"
)

// The TUI unlocks the keychain of a daemon that started with it locked, as
// nodwyn app's starts and as one the TUI starts does, over the socket: an
// empty answer leaves it locked, a wrong one is said and asked again, and
// an unlocked keychain is not asked for. Attached to such a daemon the TUI
// used to say only "Unlock the keychain first".
func TestTUIUnlocksTheDaemonsKeychain(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "nodwyn-tui-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	paths := config.PathsUnder(filepath.Join(dir, "config"), filepath.Join(dir, "data"))
	must.MkdirAll(t, filepath.Dir(paths.Keychain), 0o700)
	if _, err := keychain.Create(paths.Keychain, keychain.FastKDF, []byte("correct horse")); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "sock")
	ctx, cancel := context.WithCancel(context.Background())
	owned, done, err := ensureDaemon(ctx, paths, socket, "tui")
	if err != nil || !owned {
		t.Fatalf("no daemon started: %v", err)
	}
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	unlocked := func() bool {
		t.Helper()
		c, err := rpc.Dial(ctx, socket)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		var status struct {
			Unlocked bool `json:"unlocked"`
		}
		if err := c.Call(ctx, "keychain.status", struct{}{}, &status); err != nil {
			t.Fatal(err)
		}
		return status.Unlocked
	}
	var answers []string
	var asked int
	read := func(string) ([]byte, error) {
		asked++
		if len(answers) == 0 {
			t.Fatal("asked for a passphrase it does not need")
		}
		answer := answers[0]
		answers = answers[1:]
		return []byte(answer), nil
	}
	var out strings.Builder

	answers = []string{""}
	if err := unlockDaemon(ctx, socket, read, &out); err != nil {
		t.Fatal(err)
	}
	if unlocked() || !strings.Contains(out.String(), "left locked") {
		t.Fatalf("an empty answer did not leave it locked: %q", out.String())
	}

	answers = []string{"wrong horse", "correct horse"}
	if err := unlockDaemon(ctx, socket, read, &out); err != nil {
		t.Fatal(err)
	}
	if !unlocked() || !strings.Contains(out.String(), "does not open the keychain") {
		t.Fatalf("not unlocked after the right passphrase, or the wrong one was not said: %q", out.String())
	}

	asked = 0
	if err := unlockDaemon(ctx, socket, read, &out); err != nil || asked != 0 {
		t.Fatalf("an unlocked keychain was asked for (%d times): %v", asked, err)
	}
}
