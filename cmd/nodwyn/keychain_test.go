package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/test/fakes/must"
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

// A service manager hands a daemon nobody sits at the passphrase as the
// systemd credential nodwyn-keychain (ADR 5, amended 2026-10-17): it opens
// the keychain with no terminal; a passphrase that is wrong starts the
// daemon locked, not failed, since a service that exits is started again
// at once and could never be unlocked from a console; a directory that is
// not absolute, or has no such file, changes nothing.
func TestOpenKeychainFromASystemdCredential(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	stdin := os.Stdin
	os.Stdin = null
	defer func() { os.Stdin = stdin }()
	dir := t.TempDir()
	paths := config.PathsUnder(filepath.Join(dir, "config"), filepath.Join(dir, "data"))
	must.MkdirAll(t, filepath.Dir(paths.Keychain), 0o700)
	created, err := keychain.Create(paths.Keychain, keychain.FastKDF, []byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	created.Add("api-key", []byte("x"))
	credentials := filepath.Join(dir, "credentials")
	must.MkdirAll(t, credentials, 0o700)
	t.Setenv("CREDENTIALS_DIRECTORY", credentials)
	if store, _, err := openKeychain(paths, false); store != nil || err != nil {
		t.Fatalf("a credentials directory with no file: %v %v", store, err)
	}
	// What `echo` wrote and `systemd-creds encrypt` kept: a trailing newline.
	must.WriteFile(t, filepath.Join(credentials, credentialName), []byte("correct horse\n"), 0o400)
	store, _, err := openKeychain(paths, false)
	if err != nil || store == nil || store.Count() != 1 {
		t.Fatalf("with the credential: %v %v", store, err)
	}
	// The file is 0400, as systemd makes it: replaced here, not written to.
	must.WriteFile(t, filepath.Join(credentials, credentialName+".new"), []byte("wrong horse"), 0o400)
	if err := os.Rename(filepath.Join(credentials, credentialName+".new"), filepath.Join(credentials, credentialName)); err != nil {
		t.Fatal(err)
	}
	if store, _, err := openKeychain(paths, false); store != nil || err != nil {
		t.Fatalf("with a wrong passphrase in the credential: %v %v", store, err)
	}
	must.WriteFile(t, filepath.Join(credentials, credentialName+".new"), []byte("correct horse"), 0o400)
	if err := os.Rename(filepath.Join(credentials, credentialName+".new"), filepath.Join(credentials, credentialName)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "credentials")
	if store, _, err := openKeychain(paths, false); store != nil || err != nil {
		t.Fatalf("with a relative directory: %v %v", store, err)
	}
}
