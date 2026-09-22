package rpc

import "golang.org/x/sys/unix"

func socketPeer(fd int) (uint32, uint32, int, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, 0, 0, err
	}
	return cred.Uid, cred.Gid, int(cred.Pid), nil
}
