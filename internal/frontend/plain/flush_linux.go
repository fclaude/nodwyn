package plain

import "golang.org/x/sys/unix"

// discardTypeAhead drops what was typed on the terminal and not yet read.
func discardTypeAhead(fd int) error { return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH) }
