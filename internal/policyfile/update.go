// Package policyfile serializes and atomically publishes user-owned policy files.
// It does not interpret policy or grant authority; callers validate the contents
// and obtain approval before invoking Update.
package policyfile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Update holds a sibling advisory lock while deriving and publishing new bytes.
// The callback receives nil for a missing file; returning nil leaves it unchanged.
// Other nodwyn writers must use this lock. A final comparison detects intervening
// non-cooperating edits, but cannot make an external editor honor the lock.
func Update(ctx context.Context, path string, change func([]byte) ([]byte, error)) error {
	if path == "" {
		return fmt.Errorf("policy file: no persistence path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	fd, err := syscall.Open(path+".lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return err
	}
	lock := os.NewFile(uintptr(fd), path+".lock")
	defer lock.Close()
	if info, err := lock.Stat(); err != nil {
		return err
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("policy file: lock is not a regular file")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("policy file: refusing to replace non-regular policy file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	existed := err == nil
	next, err := change(raw)
	if err != nil {
		return err
	}
	if next == nil {
		return ctx.Err()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".policy-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(next); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Detect non-cooperating edits made while constructing the new file.
	latest, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if (err == nil) != existed || !bytes.Equal(latest, raw) {
		return fmt.Errorf("policy file: policy changed during update; retry from a new prompt")
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
