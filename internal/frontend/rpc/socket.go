package rpc

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
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

// ErrInUse is Listen's answer when something accepts connections on the
// socket: a daemon still runs there.
var ErrInUse = errors.New("a daemon is already listening on it")

// Listen must never unlink another daemon's endpoint: a socket that accepts a
// connection is refused (ErrInUse), and so is any other file. A socket that
// refuses connections was left by a daemon that died without closing it (a
// kill, an out-of-memory kill, a power loss), and is removed, so that a
// service manager can start the daemon again. The check and the bind are made
// under a lock beside the socket: without it, two daemons starting at once
// could both find the old socket stale, and the second unlink the first's new
// one. The private directory protects the lock file and the short interval
// between bind and chmod (ADR 21, amendment 2026-10-17). Supported hosts are
// Linux and macOS.
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
	// The lock is released when the file is closed; the file stays, since
	// removing it would let a third daemon lock a new file beside a waiting one.
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	if err := removeStale(path); err != nil {
		return nil, err
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

// removeStale removes the socket at path when a connection to it is refused,
// which is how the kernel answers for a socket no process listens on. Any
// other answer (a connection, a timeout, a permission error) leaves it.
func removeStale(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("rpc: %s exists and is not a socket", path)
	}
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err == nil {
		conn.Close()
		return fmt.Errorf("rpc: %s: %w", path, ErrInUse)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("rpc: %s: %w", path, err)
	}
	return os.Remove(path)
}
