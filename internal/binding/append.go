package binding

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
)

// Exact constructs a single-call proposal. It accepts no wildcard, missing
// scope, URL, or control character. Existing hand-written rows remain governed
// by Load's compatibility rules; proposals are deliberately narrower.
func Exact(class, host, method, position string) (Binding, error) {
	bad := func() (Binding, error) {
		return Binding{}, fmt.Errorf("bindings: incomplete or invalid exact call scope")
	}
	if class == "" || strings.TrimSpace(class) != class || strings.IndexFunc(class, unicode.IsControl) >= 0 {
		return bad()
	}
	if host == "" || strings.ContainsAny(host, "*/@?#[]\\") || strings.IndexFunc(host, unicode.IsSpace) >= 0 || strings.IndexFunc(host, unicode.IsControl) >= 0 {
		return bad()
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return bad()
	}
	if !token(method) {
		return bad()
	}
	parts := strings.Split(position, ":")
	switch parts[0] {
	case "body":
		if len(parts) != 1 {
			return bad()
		}
	case "header":
		if len(parts) < 2 || len(parts) > 3 || !token(parts[1]) || (len(parts) == 3 && !token(parts[2])) {
			return bad()
		}
	case "query":
		if len(parts) != 2 || parts[1] == "" || strings.IndexFunc(parts[1], unicode.IsControl) >= 0 {
			return bad()
		}
	default:
		return bad()
	}
	return Binding{Class: class, Dest: host, Methods: []string{method}, Positions: []string{position}}, nil
}
func token(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c <= 32 || c >= 127 || strings.ContainsRune("()<>@,;:\\\"/[]?={}", c) {
			return false
		}
	}
	return true
}

// AppendExact preserves the user's bytes and appends one TOML row. A sibling
// advisory lock coordinates nibb writers across processes; edits by other
// programs should use the same lock. No partial policy is ever published.
func AppendExact(ctx context.Context, path string, row Binding) error {
	if len(row.Methods) != 1 || len(row.Positions) != 1 {
		return fmt.Errorf("bindings: an exact row is required")
	}
	if _, err := Exact(row.Class, row.Dest, row.Methods[0], row.Positions[0]); err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("bindings: no persistence path")
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
		return fmt.Errorf("bindings: lock is not a regular file")
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
		return fmt.Errorf("bindings: refusing to replace non-regular policy file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	table, err := parse(raw, path)
	if err != nil {
		return err
	}
	if bound, _ := table.Lookup(row.Class, row.Dest, row.Methods[0], row.Positions[0]); bound {
		return ctx.Err()
	}
	var addition bytes.Buffer
	if err := toml.NewEncoder(&addition).Encode(Table{Bind: []Binding{row}}); err != nil {
		return err
	}
	next := append(append(append([]byte(nil), raw...), '\n'), addition.Bytes()...)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bindings-*")
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
	if !bytes.Equal(latest, raw) {
		return fmt.Errorf("bindings: policy changed during append; retry from a new prompt")
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
