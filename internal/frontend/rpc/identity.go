package rpc

import (
	"fmt"
	"net"
	"os"
)

// peerIdentity is established once at accept, from the kernel. It cannot be
// replaced by a hello label, request parameter, or reconnect cursor.
func peerIdentity(conn *net.UnixConn) (string, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return "", err
	}
	var uid, gid uint32
	var pid int
	var readErr error
	if err := raw.Control(func(fd uintptr) { uid, gid, pid, readErr = socketPeer(int(fd)) }); err != nil {
		return "", err
	}
	if readErr != nil {
		return "", readErr
	}
	if uid != uint32(os.Geteuid()) {
		return "", fmt.Errorf("rpc: peer uid does not own the daemon")
	}
	return fmt.Sprintf("unix:uid=%d,gid=%d,pid=%d", uid, gid, pid), nil
}
