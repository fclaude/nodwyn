package policyfile

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadDistinguishesMissingAndEmptyAndBoundsInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy")
	missing, err := Read(path, 4)
	if err != nil || missing != nil {
		t.Fatal(missing, err)
	}
	if err := os.WriteFile(path, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	empty, err := Read(path, 4)
	if err != nil || empty == nil || Revision(empty) == Revision(missing) {
		t.Fatal("absence confused with empty file", err)
	}
	if err := os.WriteFile(path, []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, 4); err == nil {
		t.Fatal("unbounded read")
	}
	os.Remove(path)
	target := filepath.Join(t.TempDir(), "target")
	os.WriteFile(target, []byte("data"), 0600)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, 4); err == nil {
		t.Fatal("followed symlink")
	}
	os.Remove(path)
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, 4); err == nil {
		t.Fatal("read non-regular file")
	}
}
func TestUpdateRejectsConcurrentCreationOfEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy")
	err := Update(context.Background(), path, func(raw []byte) ([]byte, error) {
		if raw != nil {
			t.Fatal("expected missing file")
		}
		if err := os.WriteFile(path, []byte{}, 0600); err != nil {
			t.Fatal(err)
		}
		return []byte("replacement"), nil
	})
	if err == nil {
		t.Fatal("overwrote concurrent creation")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) != 0 {
		t.Fatal(string(raw), err)
	}
}
