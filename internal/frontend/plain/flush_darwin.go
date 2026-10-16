package plain

import "golang.org/x/sys/unix"

// discardTypeAhead drops what was typed on the terminal and not yet read
// (FREAD: the input queue).
func discardTypeAhead(fd int) error { return unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, 1) }
