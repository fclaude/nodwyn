package termemu

import (
	"errors"
	"testing"
)

// No emulator is linked into this test binary, so this is what a build
// without one does.
func TestNewWithoutAnEmulatorSaysSo(t *testing.T) {
	if _, err := New(80, 24, 100); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("New: %v", err)
	}
}
