package log

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedClock() func() time.Time {
	t := time.Date(2026, 9, 20, 16, 0, 0, 0, time.UTC)
	return func() time.Time { t = t.Add(time.Millisecond); return t }
}

func TestRoundTrip10k(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, "s1", WithClock(fixedClock()))
	const n = 10_000
	for i := 0; i < n; i++ {
		kind := KindToolCall
		if i%2 == 1 {
			kind = KindToolResult
		}
		ev, err := w.Append(kind, map[string]any{"i": i, "name": "fs-read"})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if ev.Seq != uint64(i+1) {
			t.Fatalf("seq: want %d got %d", i+1, ev.Seq)
		}
	}
	events, err := Read(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != n {
		t.Fatalf("want %d events, got %d", n, len(events))
	}
	var last struct{ I int }
	if err := events[n-1].Decode(&last); err != nil || last.I != n-1 {
		t.Fatalf("decode last: %v %+v", err, last)
	}
	for i := 1; i < n; i++ {
		if events[i].Seq != events[i-1].Seq+1 {
			t.Fatalf("seq not monotonic at %d", i)
		}
		if !events[i].TS.After(events[i-1].TS) {
			t.Fatalf("ts not increasing at %d", i)
		}
	}
}

func TestUnknownKindRejectedOnWriteAndRead(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, "s")
	if _, err := w.Append(Kind("bogus"), nil); err == nil {
		t.Fatal("append accepted unknown kind")
	}
	buf.WriteString(`{"seq":1,"ts":"2026-09-20T16:00:00Z","session":"s","kind":"bogus"}` + "\n")
	if _, err := Read(&buf); err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("read accepted unknown kind: %v", err)
	}
}

func TestSeqGapDetected(t *testing.T) {
	in := `{"seq":1,"ts":"2026-09-20T16:00:00Z","session":"s","kind":"note"}
{"seq":3,"ts":"2026-09-20T16:00:01Z","session":"s","kind":"note"}
`
	_, err := Read(strings.NewReader(in))
	if !errors.Is(err, ErrSeqGap) {
		t.Fatalf("want ErrSeqGap, got %v", err)
	}
}

func TestTruncatedTailReturnsCompleteEvents(t *testing.T) {
	in := `{"seq":1,"ts":"2026-09-20T16:00:00Z","session":"s","kind":"note","payload":{"a":1}}
{"seq":2,"ts":"2026-09-20T16:00:01Z","session":"s","kind":"note","payload":{"a":2}}
{"seq":3,"ts":"2026-09-20T16:00:02Z","session":"s","kind":"no`
	events, err := Read(strings.NewReader(in))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 complete events, got %d", len(events))
	}
}

func TestFilterSeesEveryPayload(t *testing.T) {
	var buf bytes.Buffer
	filter := func(k Kind, p []byte) ([]byte, error) {
		return bytes.ReplaceAll(p, []byte("sk-canary-123"), []byte("[REDACTED canary]")), nil
	}
	w := New(&buf, "s", WithFilter(filter))
	if _, err := w.Append(KindUserMsg, map[string]string{"text": "key is sk-canary-123"}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("sk-canary-123")) {
		t.Fatal("secret reached the log despite filter")
	}
	if !bytes.Contains(buf.Bytes(), []byte("[REDACTED canary]")) {
		t.Fatal("placeholder missing")
	}
}

func TestFilterMustReturnJSON(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, "s", WithFilter(func(Kind, []byte) ([]byte, error) { return []byte("not json"), nil }))
	if _, err := w.Append(KindNote, map[string]int{"x": 1}); err == nil {
		t.Fatal("invalid filter output accepted")
	}
	if buf.Len() != 0 {
		t.Fatal("bad line was written")
	}
}

func TestCreateRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	p := SessionPath(dir, "s")
	w, err := Create(p, "s")
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	if _, err := Create(p, "s"); err == nil {
		t.Fatal("Create overwrote an existing log")
	}
}

func TestOpenResumesSeqAndFsyncs(t *testing.T) {
	dir := t.TempDir()
	p := SessionPath(dir, "s")
	w, err := Create(p, "s", WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := w.Append(KindNote, map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()

	w2, err := Open(p, WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}
	if w2.Session() != "s" || w2.Seq() != 3 {
		t.Fatalf("resume: session=%q seq=%d", w2.Session(), w2.Seq())
	}
	ev, err := w2.Append(KindNote, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Seq != 4 {
		t.Fatalf("resumed seq: want 4 got %d", ev.Seq)
	}
	w2.Close()

	events, err := ReadFile(p)
	if err != nil || len(events) != 4 {
		t.Fatalf("after resume: %v, %d events", err, len(events))
	}
}

func TestOpenRepairsTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	p := SessionPath(dir, "s")
	w, err := Create(p, "s", WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := w.Append(KindNote, map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	// Simulate a crash mid-write.
	f, _ := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o600)
	f.WriteString(`{"seq":3,"ts":"2026-09-20T16:00:02Z","ses`)
	f.Close()

	w2, err := Open(p, WithClock(fixedClock()))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated from Open, got %v", err)
	}
	if w2 == nil || w2.Seq() != 2 {
		t.Fatalf("writer after repair: %+v", w2)
	}
	if _, err := w2.Append(KindNote, map[string]string{"repaired": "yes"}); err != nil {
		t.Fatal(err)
	}
	w2.Close()
	events, err := ReadFile(p)
	if err != nil {
		t.Fatalf("log still damaged after repair: %v", err)
	}
	if len(events) != 3 || events[2].Seq != 3 {
		t.Fatalf("want 3 clean events, got %d", len(events))
	}
}

func TestSessionIDSortsByTime(t *testing.T) {
	a := NewSessionID(time.Date(2026, 9, 20, 1, 0, 0, 0, time.UTC))
	b := NewSessionID(time.Date(2026, 9, 20, 2, 0, 0, 0, time.UTC))
	if !(a < b) {
		t.Fatalf("ids not sortable: %s %s", a, b)
	}
	if filepath.Ext(SessionPath("/x", a)) != ".jsonl" {
		t.Fatal("session path extension")
	}
}

func TestMandatoryRedactionAfterFilter(t *testing.T) {
	var b bytes.Buffer
	w := New(&b, "test", WithFilter(func(_ Kind, _ []byte) ([]byte, error) {
		return []byte(`{"text":"\u0073k-ant-abcdefgh1234"}`), nil
	}))
	ev, err := w.Append(KindNote, Note{Text: "safe"})
	if err != nil {
		t.Fatal(err)
	}
	var note Note
	if err := ev.Decode(&note); err != nil {
		t.Fatal(err)
	}
	if note.Text != "[REDACTED anthropic-key]" {
		t.Fatalf("unfiltered event: %s", ev.Payload)
	}
	if bytes.Contains(b.Bytes(), []byte("abcdefgh")) {
		t.Fatal("canary persisted")
	}
}

func TestExclusiveWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	first, err := Create(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := first.Append(KindNote, Note{Text: "first"}); err != nil {
		t.Fatal(err)
	}
	if second, err := Open(path); err == nil {
		second.Close()
		t.Fatal("concurrent writer accepted")
	}
	first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ev, err := second.Append(KindNote, Note{Text: "second"})
	if err != nil || ev.Seq != 2 {
		t.Fatalf("append after close: %+v %v", ev, err)
	}
}

// A log closed while another goroutine forks a child opens again at once.
// The child shares the log's open file description until its exec closes
// it, and the lock went with the description: a resume during a shell
// command was refused as already open.
func TestCloseReleasesLockWhileChildrenFork(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("no true command to fork")
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	w, err := Create(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(KindNote, Note{Text: "first"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var forks sync.WaitGroup
	for range 4 {
		forks.Add(1)
		go func() {
			defer forks.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				_ = exec.Command("true").Run()
			}
		}()
	}
	defer func() { close(done); forks.Wait() }()
	for i := range 2000 {
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if w, err = Open(path); err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
	}
	w.Close()
}

func TestGatePlanSteps(t *testing.T) {
	for plan, want := range map[string][]string{
		"1. read\n2) edit\n\n  - test  \nprose in between": {"read", "edit", "test"},
		"* one\n• two":                    {"one", "two"},
		"Just do it\nand check.":          {"Just do it and check."},
		"":                                nil,
		"10. tenth\nnot a step 3. inline": {"tenth"},
	} {
		got := GatePlan{Plan: plan}.Steps()
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%q: %q, want %q", plan, got, want)
		}
	}
}
