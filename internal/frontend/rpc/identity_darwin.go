package rpc

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func socketPeer(fd int) (uint32, uint32, int, error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, 0, 0, err
	}
	if cred.Ngroups < 1 {
		return 0, 0, 0, fmt.Errorf("rpc: peer has no effective group")
	}
	pid, err := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	return cred.Uid, cred.Groups[0], pid, err
}
