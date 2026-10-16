package log

import (
	"errors"
	"path/filepath"
	"testing"
)

// A file is cut back to where it was before a line that failed, so the
// next event is written whole and the log reads as if the failed one was
// never tried.
func TestAFailedLineIsCutFromTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	w, err := Create(path, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Append(KindNote, Note{Text: "first"}); err != nil {
		t.Fatal(err)
	}
	file := w.w.(cuttable)
	st, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	size := st.Size()
	if _, err := w.w.Write([]byte(`{"seq":2,"half`)); err != nil {
		t.Fatal(err)
	}
	if err := w.undo(file, size, errors.New("no space left on device")); err == nil || w.broken != nil {
		t.Fatalf("undo: %v, broken %v", err, w.broken)
	}
	if _, err := w.Append(KindNote, Note{Text: "second"}); err != nil {
		t.Fatal(err)
	}
	events, err := ReadFile(path)
	if err != nil || len(events) != 2 || events[1].Seq != 2 {
		t.Fatalf("the log after a cut line: %v, %v", events, err)
	}
}
