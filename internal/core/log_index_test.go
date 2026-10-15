package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
)

// fullPage is a page of a log read the way ReadLog read every page before
// the index (ADR 49 §10): the whole log, then the events wanted.
func fullPage(t *testing.T, path string, after, through uint64, limit int) (LogPage, error) {
	t.Helper()
	history, err := log.ReadFile(path)
	truncated := errors.Is(err, log.ErrTruncated)
	if err != nil && !truncated {
		return LogPage{}, err
	}
	last := history[len(history)-1].Seq
	if through == 0 {
		through = last
	}
	if through > last || after > through {
		return LogPage{}, fmt.Errorf("core: log cursor is beyond the available history")
	}
	page := LogPage{Events: []log.Event{}, Next: after, Through: through, Truncated: truncated}
	for _, ev := range history {
		if ev.Seq <= after {
			continue
		}
		if ev.Seq > through || len(page.Events) == limit {
			break
		}
		if len(ev.Payload) > 0 {
			if ev.Payload, _, err = redact.JSON(ev.Payload, nil); err != nil {
				return LogPage{}, err
			}
		}
		page.Events = append(page.Events, ev)
		page.Next = ev.Seq
	}
	page.More = page.Next < through
	return page, nil
}

// sameAsFullRead fails unless the store's listing and pages of the log at
// id, read through its index, are what reading the whole log gives.
func sameAsFullRead(t *testing.T, st Store, id, when string) {
	t.Helper()
	path := log.SessionPath(st.Dir, id)
	forget(path)
	got, err := st.Info(id)
	if err != nil {
		t.Fatalf("%s: Info: %v", when, err)
	}
	want, _, err := st.read(id)
	if err != nil {
		t.Fatalf("%s: read: %v", when, err)
	}
	if a, b := asJSON(t, got), asJSON(t, want); a != b {
		t.Fatalf("%s: the listing from the index\n%s\nis not the whole log's\n%s", when, a, b)
	}
	last := lastSeq(t, path)
	for _, c := range []struct {
		after, through uint64
		limit          int
	}{{0, 0, 1}, {0, 0, 1000}, {1, 0, 7}, {last - 1, 0, 5}, {last, 0, 3}, {markEvery - 1, 0, 2}, {markEvery, 0, 300}, {markEvery + 1, last, 1000}, {3, 9, 100}, {last / 2, last/2 + 40, 10}} {
		if c.after > last || (c.through != 0 && (c.through > last || c.after > c.through)) {
			continue
		}
		gotPage, gotErr := st.ReadLog(id, c.after, c.through, c.limit)
		wantPage, wantErr := fullPage(t, path, c.after, c.through, c.limit)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%s: page %+v: %v, want %v", when, c, gotErr, wantErr)
		}
		if a, b := asJSON(t, gotPage), asJSON(t, wantPage); a != b {
			t.Fatalf("%s: page %+v from the index\n%.600s\nis not the whole log's\n%.600s", when, c, a, b)
		}
	}
}

func lastSeq(t *testing.T, path string) uint64 {
	t.Helper()
	evs, err := log.ReadFile(path)
	if err != nil && !errors.Is(err, log.ErrTruncated) {
		t.Fatal(err)
	}
	return evs[len(evs)-1].Seq
}

// writeLog appends events to the log at path without syncing each one,
// with every kind the listing reads among them.
type syntheticLog struct {
	t    *testing.T
	w    *log.Writer
	f    *os.File
	repo string
	n    int
}

func newSyntheticLog(t *testing.T, dir, id string) *syntheticLog {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(log.SessionPath(dir, id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	// A writer that is not a file is not synced after every event.
	l := &syntheticLog{t: t, f: f, w: log.New(struct{ io.Writer }{f}, id), repo: t.TempDir()}
	l.add(log.KindGuardDecision, log.GuardDecision{Prim: "load", Origin: "user", Decision: "allow"})
	l.add(log.KindSessionStart, log.SessionStart{Repo: l.repo, Worktree: &log.Worktree{Repository: l.repo, Path: filepath.Join(l.repo, ".nodwyn", "worktrees", "x"), Branch: "nodwyn/x", Base: "main", BaseCommit: "a1"}})
	return l
}

func (l *syntheticLog) add(kind log.Kind, payload any) {
	l.t.Helper()
	if _, err := l.w.Append(kind, payload); err != nil {
		l.t.Fatal(err)
	}
}

// turns appends n turns of the kinds a listing reads: a request, a call and
// its write, a reply, and now and then a name, an archive, a run's state or
// a worktree made again.
func (l *syntheticLog) turns(n int) {
	for range n {
		l.n++
		i := l.n
		l.add(log.KindUserMsg, log.UserMsg{Text: fmt.Sprintf("request %d. With more after it", i)})
		l.add(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: "c", Name: "fs-write", Args: json.RawMessage(`{}`)}}})
		l.add(log.KindGuardDecision, log.GuardDecision{Prim: "fs-write", Origin: "model", Decision: []string{"allow", "confirm", "deny"}[i%3], Answer: "yes", Path: fmt.Sprintf("file-%d.txt", i%60)})
		l.add(log.KindToolResult, log.ToolResult{ID: "c", Name: "fs-write", OK: true})
		if i%4 != 0 {
			l.add(log.KindLLMResp, log.LLMResp{Content: "done"})
			l.add(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
		}
		switch i % 7 {
		case 1:
			l.add(log.KindSessionTitle, log.SessionTitle{Title: fmt.Sprintf("name %d", i)})
		case 2:
			l.add(log.KindSessionArchive, log.SessionArchive{Archived: i%2 == 0})
		case 3:
			l.add(log.KindRunState, log.RunState{Task: fmt.Sprintf("task %d", i), MaxTurns: 3})
		case 4:
			l.add(log.KindWorktreeRestore, log.WorktreeChange{Worktree: log.Worktree{Repository: l.repo, Path: filepath.Join(l.repo, ".nodwyn", "worktrees", "x"), Branch: "nodwyn/x", Base: "main", BaseCommit: fmt.Sprintf("b%d", i)}})
		case 5:
			l.add(log.KindSessionResume, log.SessionStart{Repo: l.repo})
		}
	}
}

// A listing and every page read through a log's index are what reading the
// whole log gives (ADR 49 §10): as the log grows, folded in from where the
// index ended; with the index deleted, or not this log's (its last line's
// hash, where it ends, a mark, its JSON); with a torn last line; for the
// recorded sessions in test/sessions; and the index goes when the session is
// deleted. What the index derived is not checked against the whole log,
// which would read it all: whatever could change it could change the log.
func TestLogIndexReadsWhatTheWholeLogSays(t *testing.T) {
	t.Parallel()
	st := Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	id := "20261015T120000Z-0000aaaa"
	l := newSyntheticLog(t, st.Dir, id)
	for _, grow := range []int{1, 3, 40, 50, 1, 200} {
		l.turns(grow)
		sameAsFullRead(t, st, id, fmt.Sprintf("after %d turns", l.n))
	}
	idxPath := indexPath(st.Dir, id)
	if _, err := os.Stat(idxPath); err != nil {
		t.Fatalf("no index was written: %v", err)
	}

	if err := os.Remove(idxPath); err != nil {
		t.Fatal(err)
	}
	sameAsFullRead(t, st, id, "the index deleted")

	var idx logIndex
	edit := func(what string, change func()) {
		t.Helper()
		raw, err := os.ReadFile(idxPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &idx); err != nil {
			t.Fatal(err)
		}
		change()
		if raw, err = json.Marshal(idx); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(idxPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		l.turns(1)
		sameAsFullRead(t, st, id, what)
	}
	edit("the last line's hash wrong", func() { idx.Hash = strings.Repeat("0", 64) })
	edit("an index that ends past the log", func() { idx.Start, idx.End = idx.Start+1<<20, idx.End+1<<20 })
	edit("a mark in the middle of a line", func() { idx.Marks[1] += 7 })
	edit("a mark at another event's line", func() { idx.Marks[2] = idx.Marks[1] })
	if err := os.WriteFile(idxPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	sameAsFullRead(t, st, id, "an index that is not JSON")

	if _, err := l.f.WriteString(`{"seq":`); err != nil {
		t.Fatal(err)
	}
	sameAsFullRead(t, st, id, "a torn last line")
	if info, _ := st.Info(id); !info.Truncated {
		t.Fatal("a torn last line was not reported")
	}
	l.f.Close()
	if err := st.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(idxPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the index outlived its session: %v", err)
	}

	fixtures, err := filepath.Glob("../../test/sessions/*.jsonl")
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no recorded sessions: %v", err)
	}
	for _, fixture := range fixtures {
		evs, err := log.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(log.SessionPath(st.Dir, evs[0].Session), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		sameAsFullRead(t, st, evs[0].Session, filepath.Base(fixture))
		sameAsFullRead(t, st, evs[0].Session, filepath.Base(fixture)+", read again")
	}
}
