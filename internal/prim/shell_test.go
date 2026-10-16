package prim

import (
	"bytes"
	"strings"
	"testing"
)

// Output that reaches the cap exactly at the end of one write, as io.Copy's
// 32 KiB chunks reach shell-exec's 256 KiB, still says it was cut when more
// comes: it used to drop the rest silently, the failing tail included.
func TestCappedOutputSaysItWasCut(t *testing.T) {
	var buf bytes.Buffer
	c := &capped{buf: &buf, n: 8}
	c.Write([]byte("1234"))
	c.Write([]byte("5678"))
	c.Write([]byte("tail"))
	c.Write([]byte("more"))
	if got := buf.String(); got != "12345678\n[output truncated at 8 B]\n" {
		t.Fatalf("%q", got)
	}
	buf.Reset()
	c = &capped{buf: &buf, n: 8}
	c.Write([]byte("1234"))
	if !strings.HasSuffix(buf.String(), "1234") || strings.Contains(buf.String(), "truncated") {
		t.Fatalf("output under the cap: %q", buf.String())
	}
}
