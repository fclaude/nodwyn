package policyfile

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// Revision distinguishes a missing file (nil) from an empty file. It is a
// comparison token, never authority to write the file.
func Revision(raw []byte) string {
	if raw == nil {
		return "missing"
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

// Read returns a bounded regular file without following a final symlink.
// Missing files return nil so an editor can explicitly review their creation.
func Read(path string, limit int64) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("policy file is not a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("policy file exceeds %d bytes", limit)
	}
	return raw, nil
}
