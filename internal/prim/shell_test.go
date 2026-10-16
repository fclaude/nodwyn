package prim

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/scheme"
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

// A writer with a tail keeps the first and the last of what it is given, with
// the marker between them once finished.
func TestCappedWithATailKeepsBothEnds(t *testing.T) {
	var buf bytes.Buffer
	c := &capped{buf: &buf, n: 8, tail: 4}
	for _, w := range []string{"12", "3456", "789a", "bcde", "fgh"} {
		c.Write([]byte(w))
	}
	c.finish()
	if got := buf.String(); !strings.HasPrefix(got, "1234\n[") || !strings.HasSuffix(got, "]\nefgh") || !strings.Contains(got, "9 B omitted") {
		t.Fatalf("%q", got)
	}
	buf.Reset()
	c = &capped{buf: &buf, n: 8, tail: 4}
	c.Write([]byte("1234567"))
	c.finish()
	if buf.String() != "1234567" {
		t.Fatalf("output under the cap: %q", buf.String())
	}
}

func shellExecWith(t *testing.T, mod func(*Options)) *scheme.Interp {
	t.Helper()
	o := DefaultOptions(t.TempDir())
	mod(&o)
	in := scheme.New()
	if err := Register(in, o); err != nil {
		t.Fatal(err)
	}
	return in
}

// A command that runs past the limit used to return only "timed out", with
// nothing of what it printed: the output says where a hung test run stopped.
func TestShellExecTimeoutReturnsWhatItPrinted(t *testing.T) {
	in := shellExecWith(t, func(o *Options) { o.ShellTimeout = 500 * time.Millisecond })
	v, err := in.EvalString(context.Background(), `(shell-exec "sh" '("-c" "echo FAIL TestSomething; echo oops >&2; sleep 30"))`)
	if err != nil {
		t.Fatal(err)
	}
	got := scheme.Write(v)
	for _, want := range []string{`("status" . "timed out after 500ms")`, "FAIL TestSomething", "oops"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}

// Output past the cap used to keep its first 256 KiB and drop the rest, the
// failure at the end of a long build with it.
func TestShellExecKeepsTheEndOfLongOutput(t *testing.T) {
	in := shellExecWith(t, func(o *Options) { o.MaxOutput = 4096 })
	v, err := in.EvalString(context.Background(), `(shell-exec "sh" '("-c" "seq 1 20000; echo FAILED at the end"))`)
	if err != nil {
		t.Fatal(err)
	}
	got := scheme.Write(v)
	if !strings.Contains(got, `"1\n2\n3\n`) || !strings.Contains(got, "FAILED at the end") || !strings.Contains(got, "omitted") {
		t.Fatalf("%.300s ... %s", got, got[max(0, len(got)-200):])
	}
	if len(got) > 2*4096 {
		t.Fatalf("answer is %d bytes", len(got))
	}
}
