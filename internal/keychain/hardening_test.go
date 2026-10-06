package keychain

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestKeychainRejectsHostileKDFHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keychain")
	if _, err := Create(path, FastKDF, []byte("test-only")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []KDFParams{{0, 8192, 1}, {1, 8192, 0}, {11, 8192, 1}, {1, 262145, 1}, {1, 8192, 17}, {1, 7, 1}, {10, 262144, 1}} {
		bad := bytes.Clone(raw)
		binary.LittleEndian.PutUint32(bad[len(magic):], p.Time)
		binary.LittleEndian.PutUint32(bad[len(magic)+4:], p.Memory)
		bad[len(magic)+8] = p.Threads
		if _, err := split(bad); err == nil {
			t.Errorf("accepted unbounded parameters: %+v", p)
		}
	}
	// Opening the zero-time header used to panic before it could authenticate.
	binary.LittleEndian.PutUint32(raw[len(magic):], 0)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, []byte("test-only")); err == nil {
		t.Fatal("opened invalid KDF header")
	}
}

func TestKeychainRejectsOversizedFilesBeforeDerivation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keychain")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(17 << 20); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, nil); err == nil || !bytes.Contains([]byte(err.Error()), []byte("16 MiB")) {
		t.Fatalf("size refusal: %v", err)
	}
}

func TestKeychainSaveDoesNotFollowPredictableTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path, victim := filepath.Join(dir, "keychain"), filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep me"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	st, err := Create(path, FastKDF, []byte("test-only"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add("test-key", []byte("test-canary")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != "keep me" {
		t.Fatalf("temporary symlink overwrote victim: %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("keychain permissions: %v, %v", info, err)
	}
	opened, err := Open(path, []byte("test-only"))
	if err != nil || opened.Count() != 1 {
		t.Fatalf("persisted store: %v", err)
	}
	left, err := filepath.Glob(filepath.Join(dir, ".keychain-*"))
	if err != nil || len(left) != 0 {
		t.Fatalf("temporary files left: %v, %v", left, err)
	}
}

func TestKeychainCreateRejectsInvalidParameters(t *testing.T) {
	if _, err := Create(filepath.Join(t.TempDir(), "keychain"), KDFParams{}, nil); err == nil {
		t.Fatal("creation accepted zero Argon2 parameters")
	}
}

func TestFailedKeychainSaveRestoresEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keychain")
	st, err := Create(path, FastKDF, []byte("test-only"))
	if err != nil {
		t.Fatal(err)
	}
	// Base64 in the sealed document would exceed the supported file size.
	if _, err := st.Add("too-large", make([]byte, 13<<20)); err == nil {
		t.Fatal("oversized credential was saved")
	}
	if st.Count() != 0 {
		t.Fatal("failed save retained an unpersisted entry in memory")
	}
	opened, err := Open(path, []byte("test-only"))
	if err != nil || opened.Count() != 0 {
		t.Fatalf("failed save changed the file: %v", err)
	}
}
