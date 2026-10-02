package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
)

// A keychain without a passphrase opens where nobody can type one, as an
// unattended run or a daemon started by a service manager; one with a
// passphrase still starts the session without credentials there.
func TestOpenKeychainWithoutATerminal(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	stdin := os.Stdin
	os.Stdin = null // even when go test runs at a terminal
	defer func() { os.Stdin = stdin }()
	dir := t.TempDir()
	paths := config.PathsUnder(filepath.Join(dir, "config"), filepath.Join(dir, "data"))
	os.MkdirAll(filepath.Dir(paths.Keychain), 0o700)
	if store, _, err := openKeychain(paths, false); store != nil || err != nil {
		t.Fatalf("no keychain: %v %v", store, err)
	}
	created, err := keychain.Create(paths.Keychain, keychain.FastKDF, nil)
	if err != nil {
		t.Fatal(err)
	}
	created.Add("api-key", []byte("x"))
	store, _, err := openKeychain(paths, false)
	if err != nil || store == nil || store.Count() != 1 {
		t.Fatalf("without a passphrase: %v %v", store, err)
	}
	if err := store.SetPassphrase([]byte("correct horse")); err != nil {
		t.Fatal(err)
	}
	if store, _, err := openKeychain(paths, false); store != nil || err != nil {
		t.Fatalf("with a passphrase and no terminal: %v %v", store, err)
	}
}
