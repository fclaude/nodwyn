package prim

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// A ranged read returns a header and the lines exactly as in the file, line
// endings included, so a copy matches for fs-edit; it never returns more
// than the range budget, stopping at a whole line.
func TestReadRanges(t *testing.T) {
	h := newHarness(t)
	var b strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&b, "line %04d\n", i) // 10 bytes each
	}
	os.WriteFile(filepath.Join(h.root, "big.txt"), []byte(b.String()), 0o644)
	os.WriteFile(filepath.Join(h.root, "crlf.txt"), []byte("a\r\nb\r\nc\r\n"), 0o644)
	os.WriteFile(filepath.Join(h.root, "open.txt"), []byte("one\ntwo"), 0o644)
	os.WriteFile(filepath.Join(h.root, "long.txt"), []byte(strings.Repeat("é", 6000)+"\nshort\n"), 0o644)
	os.WriteFile(filepath.Join(h.root, "bad.txt"), []byte("ok\n\xff\xfe\nok\n"), 0o644)
	os.WriteFile(filepath.Join(h.root, "empty.txt"), nil, 0o644)

	for src, want := range map[string]string{
		`(fs-read "big.txt" 1 2)`:    "[lines 1-2 of 1000]\nline 0001\nline 0002\n",
		`(fs-read "big.txt" 500 3)`:  "[lines 500-502 of 1000]\nline 0500\nline 0501\nline 0502\n",
		`(fs-read "big.txt" 999 10)`: "[lines 999-1000 of 1000]\nline 0999\nline 1000\n",
		`(fs-read "crlf.txt" 2 1)`:   "[lines 2-2 of 3]\nb\r\n",
		`(fs-read "open.txt" 2)`:     "[lines 2-2 of 2]\ntwo",
		`(fs-read "bad.txt" 2 1)`:    "[lines 2-2 of 3]\n\xff\xfe\n",
		`(fs-read "long.txt" 2 1)`:   "[lines 2-2 of 2]\nshort\n",
	} {
		if got := h.mustEval(t, src).S; got != want {
			t.Errorf("%s:\n%q\nwant\n%q", src, got, want)
		}
	}
	// The default count is 200 lines, within the budget here (2 KB).
	if got := h.mustEval(t, `(fs-read "big.txt" 101)`).S; !strings.HasPrefix(got, "[lines 101-300 of 1000]\nline 0101\n") {
		t.Errorf("default count: %.40q", got)
	}
	// A range larger than the budget stops at a whole line and says where.
	got := h.mustEval(t, `(fs-read "big.txt" 1 1000)`).S
	budget := DefaultOptions(h.root).MaxRangeBytes
	body := got[strings.Index(got, "\n")+1:]
	if len(body) > budget || !strings.HasSuffix(body, "\n") || !strings.HasPrefix(got, fmt.Sprintf("[lines 1-%d of 1000]", len(body)/10)) {
		t.Errorf("budgeted range: %d bytes, header %.30q", len(body), got)
	}
	// One line longer than the budget is cut at a character boundary.
	got = h.mustEval(t, `(fs-read "long.txt" 1 1)`).S
	if !strings.HasPrefix(got, fmt.Sprintf("[line 1 of 2, cut at %d bytes]\n", budget)) || !utf8.ValidString(got) {
		t.Errorf("cut line: %.60q", got)
	}
	for src, want := range map[string]string{
		`(fs-read "big.txt" 1001)`: "start 1001 is past the end of big.txt (1000 lines)",
		`(fs-read "big.txt" 0)`:    "start is a 1-based line number",
		`(fs-read "big.txt" 1 0)`:  "count must be at least 1",
		`(fs-read "empty.txt" 1)`:  "empty.txt is empty",
		`(fs-read "big.txt" "1")`:  "expected integer",
	} {
		if _, err := h.eval(t, src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
	// The whole-file read is unchanged, and too large a file says to use ranges.
	if got := h.mustEval(t, `(fs-read "crlf.txt")`).S; got != "a\r\nb\r\nc\r\n" {
		t.Errorf("whole read: %q", got)
	}
	os.WriteFile(filepath.Join(h.root, "huge.txt"), []byte(strings.Repeat("x\n", 600_000)), 0o644)
	if _, err := h.eval(t, `(fs-read "huge.txt")`); err == nil || !strings.Contains(err.Error(), "read it in ranges with start and count") {
		t.Errorf("huge whole read: %v", err)
	}
	if got := h.mustEval(t, `(fs-read "huge.txt" 599999 5)`).S; got != "[lines 599999-600000 of 600000]\nx\nx\n" {
		t.Errorf("ranged read past MaxReadBytes: %q", got)
	}
	// The repository pin applies to ranged reads as to whole ones.
	if _, err := h.eval(t, `(fs-read "/etc/passwd" 1 1)`); err == nil {
		t.Error("a ranged read escaped the repository")
	}
}
