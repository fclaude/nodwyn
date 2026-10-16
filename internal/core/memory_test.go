package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
)

// appendEvents appends events to a session's log, as the session that
// holds it would, numbering them after its last.
func appendEvents(t *testing.T, o Options, id string, add func(write func(log.Kind, any))) {
	t.Helper()
	path := log.SessionPath(o.Paths.SessionsDir, id)
	evs, err := log.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	seq := evs[len(evs)-1].Seq
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	add(func(kind log.Kind, payload any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		seq++
		line, err := json.Marshal(log.Event{Seq: seq, TS: time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC).Add(time.Duration(seq) * time.Second), Session: id, Kind: kind, Payload: raw})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	})
}

// pagerTurn is a turn that fixed a flaky test.
func pagerTurn(write func(log.Kind, any)) {
	write(log.KindUserMsg, log.UserMsg{Text: "Fix the flaky TestPagerOrder"})
	write(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: "g", Name: "shell-exec", Args: json.RawMessage(`{"cmd":"go","args":["test","./pager/"]}`)}}})
	write(log.KindToolCall, log.ToolCallEvent{ToolCall: log.ToolCall{ID: "g", Name: "shell-exec", Args: json.RawMessage(`{"cmd":"go","args":["test","./pager/"]}`)}})
	write(log.KindToolResult, log.ToolResult{ID: "g", Name: "shell-exec", OK: true, Value: `(("status" . 1) ("stdout" . "connection reset by peer"))`})
	write(log.KindGuardDecision, log.GuardDecision{Prim: "fs-edit", Decision: "allow", Path: "pager/pager.go"})
	write(log.KindLLMResp, log.LLMResp{Content: "Sorted the keys in pager.go."})
	write(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
}

// wholeMemory is what folding the whole log from its start, in memory,
// makes of it.
func wholeMemory(t *testing.T, o Options, id string) *logMemory {
	t.Helper()
	evs, err := log.ReadFile(log.SessionPath(o.Paths.SessionsDir, id))
	if err != nil && !errors.Is(err, log.ErrTruncated) {
		t.Fatal(err)
	}
	l, err := foldEvents(id, evs)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// sameMemory fails unless a and b hold the same cards and episodes, the
// same documents in each part, and rank query alike.
func sameMemory(t *testing.T, when, query string, a, b *logMemory) {
	t.Helper()
	if x, y := asJSON(t, a.entries), asJSON(t, b.entries); x != y {
		t.Fatalf("%s: entries\n%s\nare not the whole log's\n%s", when, x, y)
	}
	if len(a.parts) != len(b.parts) || a.open != b.open || a.last != b.last {
		t.Fatalf("%s: %d parts, open %d, last %d; the whole log's %d, %d, %d", when, len(a.parts), a.open, a.last, len(b.parts), b.open, b.last)
	}
	for i := range a.parts {
		if !slices.Equal(a.parts[i].seqs, b.parts[i].seqs) || !slices.EqualFunc(a.parts[i].raw, b.parts[i].raw, bytes.Equal) {
			t.Fatalf("%s: part %d holds %v, the whole log's %v", when, i, a.parts[i].seqs, b.parts[i].seqs)
		}
	}
	x, _ := rankTurns([]*logMemory{a}, query, 20)
	y, _ := rankTurns([]*logMemory{b}, query, 20)
	if asJSON(t, fmt.Sprint(x)) != asJSON(t, fmt.Sprint(y)) {
		t.Fatalf("%s: %q ranks\n%v\nnot as the whole log does\n%v", when, query, x, y)
	}
}

// A person searches this repository's tasks from a terminal as the model
// searches earlier tasks (ADR 50 §9, ADR 51): the events found are shown
// under their turns, each turn in a line after its task, saying how it
// ended and what it asked; a compaction's note says what it is; another
// repository's task, which says the same, is never found; --task keeps to
// one. Each log's words are then beside it in its .mem.
func TestSearchShowsEventsUnderTheirTurns(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	project := config.CanonicalPath(o.Repo)
	pager, plain := "20261001T070000Z-0000aa01", "20261002T070000Z-0000aa02"
	longLog(t, o, pager, 30, 25, pagerTurn)
	longLog(t, o, plain, 5, 100, nil)
	other := o
	other.Repo = t.TempDir()
	longLog(t, other, "20261003T070000Z-0000bb01", 3, 100, pagerTurn)
	st := Store{Dir: o.Paths.SessionsDir}

	got, err := st.SearchTasks(project, "", "flaky TestPagerOrder connection reset", 8)
	if err != nil {
		t.Fatal(err)
	}
	turn := fmt.Sprintf("task %s, \"run 0: look at last night's failures\" (2026-09-01), turn seq 158-164 · completed · 6s · asked \"Fix the flaky TestPagerOrder\"\n", pager)
	result := `  seq 161, the result of shell-exec {"args":["test","./pager/"],"cmd":"go"}` + "\n    1: ((\"status\" . 1) (\"stdout\" . \"connection reset by peer\"))\n"
	if !strings.HasPrefix(got, "[the 4 best of ") || !strings.Contains(got, "in this repository's 2 tasks") || !strings.Contains(got, turn+"  seq 158, the person's message\n") || !strings.Contains(got, result) {
		t.Fatalf("the search:\n%s", got)
	}
	if strings.Contains(got, "bb01") {
		t.Fatalf("another repository's task was found:\n%s", got)
	}
	notes, err := st.SearchTasks(project, "", "the note: what the runs so far did", 3)
	if err != nil || !strings.Contains(notes, ", a reply, the note of a compaction of seq 1 to 132\n") {
		t.Fatalf("the compaction's note: %v\n%s", err, notes)
	}
	if only, err := st.SearchTasks(project, plain, "TestPagerOrder", 8); err != nil || !strings.HasPrefix(only, "No event of this repository's 1 tasks holds") {
		t.Fatalf("one task only: %v\n%s", err, only)
	}
	for _, id := range []string{pager, plain} {
		if _, err := os.Stat(memPath(st.Dir, id)); err != nil {
			t.Fatalf("the words of %s are not beside its log: %v", id, err)
		}
	}
}

// A session's memory is kept in <id>.mem and folded from where it ended:
// what it holds as the log grows, an unfinished turn included, is what
// folding the whole log gives, and it ranks alike, read whole or with only
// the query's words; it covers only whole turns, and what it kept is not
// made again when the log grows; one that does not match its log, or whose
// words are cut short, or that is gone, is made again; deleting the
// session deletes it.
func TestMemoryIsFoldedFromTheEndAndChecked(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	id := "20261001T070000Z-0000cc01"
	longLog(t, o, id, 3, 100, func(write func(log.Kind, any)) {
		write(log.KindUserMsg, log.UserMsg{Text: "an unfinished turn"})
		write(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: "c", Name: "fs-list", Args: json.RawMessage(`{}`)}}})
		write(log.KindToolResult, log.ToolResult{ID: "c", Name: "fs-list", OK: true, Value: "listing what is under way"})
	})
	st := Store{Dir: o.Paths.SessionsDir}
	same := func(when string) {
		t.Helper()
		// The second query's words are only in documents the file kept
		// before, so a file written back with only the first's words
		// ranks it otherwise.
		for _, query := range []string{"listing run failures", "done look night under way"} {
			got, err := st.memory(id, queryWords(query))
			if err != nil {
				t.Fatalf("%s: %v", when, err)
			}
			sameMemory(t, when, query, got, wholeMemory(t, o, id))
		}
	}
	same("with a turn under way")
	header := func() memFile {
		t.Helper()
		raw, err := os.ReadFile(memPath(st.Dir, id))
		if err != nil {
			t.Fatal(err)
		}
		line, _, _ := bytes.Cut(raw, []byte("\n"))
		var m memFile
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := header(); m.Seq != 17 || m.Docs != 12 {
		t.Fatalf("the memory covers through seq %d with %d documents; the last whole turn ends at 17, after 12", m.Seq, m.Docs)
	}
	appendEvents(t, o, id, func(write func(log.Kind, any)) {
		write(log.KindTurnEnd, log.TurnEnd{Outcome: "stopped"})
		pagerTurn(write)
	})
	same("after the log grew")
	if m := header(); m.Seq != 28 || len(m.Entries) != 5 {
		t.Fatalf("the grown memory covers through seq %d with %d entries; want 28 and 5 turns", m.Seq, len(m.Entries))
	}
	raw, err := os.ReadFile(memPath(st.Dir, id))
	if err != nil {
		t.Fatal(err)
	}
	line, words, _ := bytes.Cut(raw, []byte("\n"))
	for when, broken := range map[string][]byte{
		"with a memory file that is not this log's": append(bytes.Replace(line, []byte(`"hash":"`), []byte(`"hash":"0`), 1), append([]byte("\n"), words...)...),
		"with its words cut short":                  raw[:len(raw)-3],
	} {
		if err := os.WriteFile(memPath(st.Dir, id), broken, 0o600); err != nil {
			t.Fatal(err)
		}
		same(when)
	}
	if err := os.Remove(memPath(st.Dir, id)); err != nil {
		t.Fatal(err)
	}
	same("with the memory file deleted")

	// A document planted in what the file kept is still there after the
	// log grew: the read folded in only the turn the log gained.
	raw, err = os.ReadFile(memPath(st.Dir, id))
	if err != nil {
		t.Fatal(err)
	}
	line, words, _ = bytes.Cut(raw, []byte("\n"))
	kept, _, err := readWords(words, nil)
	if err != nil {
		t.Fatal(err)
	}
	kept.raw[0] = []byte(`{"what":"planted"}`)
	if err := os.WriteFile(memPath(st.Dir, id), appendWords(append(line, '\n'), kept), 0o600); err != nil {
		t.Fatal(err)
	}
	appendEvents(t, o, id, pagerTurn)
	grown, err := st.memory(id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := grown.parts[0].doc(0); d.What != "planted" || grown.parts[0].seqs[len(grown.parts[0].seqs)-1] != 34 {
		t.Fatalf("the grown memory starts with %q and ends at seq %d: it was made again, or not grown", d.What, grown.parts[0].seqs[len(grown.parts[0].seqs)-1])
	}
	if err := st.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(memPath(st.Dir, id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the memory file outlived its session: %v", err)
	}
}

// Recall of a turn's first message shows what the turn did, from its card
// but for what it asked, and the seq of each event it holds; of another
// repository's task it is refused (ADR 51).
func TestRecallOfATurnsMessageShowsTheTurn(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	project := config.CanonicalPath(o.Repo)
	id := "20261001T070000Z-0000ee01"
	longLog(t, o, id, 6, 4, pagerTurn)
	other := o
	other.Repo = t.TempDir()
	foreign := "20261003T070000Z-0000ee02"
	longLog(t, other, foreign, 1, 100, nil)
	st := Store{Dir: o.Paths.SessionsDir}
	got, err := st.RecallTask(project, id, 38, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`[task %s, "run 0: look at last night's failures" (2026-09-01): seq 38, the person's message]`, id) + `
[lines 1-11 of 11]
Fix the flaky TestPagerOrder

[what the turn of seq 38 to 44 did]
outcome completed · 6s
wrote   pager/pager.go
ran     go test ./pager/ (exit 1)
answer  Sorted the keys in pager.go.
[the events it holds]
seq 39, a reply
seq 41, the result of shell-exec {"args":["test","./pager/"],"cmd":"go"}
seq 43, a reply`
	if got != want {
		t.Fatalf("the turn:\n%s\nwant\n%s", got, want)
	}
	if _, err := st.RecallTask(project, foreign, 3, 0, 0); err == nil || !strings.Contains(err.Error(), "is not one of this project's") {
		t.Fatalf("another repository's task was recalled: %v", err)
	}
}
