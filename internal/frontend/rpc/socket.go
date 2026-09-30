package rpc

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// DefaultSocket uses XDG on Linux and the user's cache directory when no
// runtime directory is supplied (including macOS). The dedicated parent is
// private; an existing insecure directory is rejected, never chmodded for you.
func DefaultSocket() (string, error) {
	root := os.Getenv("XDG_RUNTIME_DIR")
	if root == "" {
		var err error
		root, err = os.UserCacheDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(root, "nodwyn", "run")
	} else {
		root = filepath.Join(root, "nodwyn")
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("rpc: runtime directory must be absolute")
	}
	return filepath.Join(root, "sock"), nil
}

// Listen refuses existing sockets, including stale ones: it must never unlink
// another daemon's endpoint. The private directory protects the short interval
// between bind and chmod. Supported hosts are Linux and macOS.
func Listen(path string) (*net.UnixListener, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("rpc: socket path must be absolute")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("rpc: socket directory must be owned by this user, mode 0700, and not a symlink: %s", dir)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}
