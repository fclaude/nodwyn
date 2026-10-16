// Package must is test setup that fails the test when it fails. A test that
// builds its world with os.WriteFile, os.Symlink and the like and drops the
// errors can pass on a state that never existed: the exfiltration scenarios
// that read a canary file stay green when the file was never written,
// since the guard refuses by path before it opens anything. Use these for
// every file, folder and link a test depends on.
package must

import (
	"os"
	"testing"
)

// WriteFile writes data to path, which must succeed.
func WriteFile(t testing.TB, path string, data []byte, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, perm); err != nil {
		t.Fatalf("setup: %v", err)
	}
}

// MkdirAll makes the folder and its parents, which must succeed.
func MkdirAll(t testing.TB, path string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, perm); err != nil {
		t.Fatalf("setup: %v", err)
	}
}

// Symlink makes the link newname to oldname, which must succeed.
func Symlink(t testing.TB, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Fatalf("setup: %v", err)
	}
}
