package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/memory"
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

// freshMemory is what folding the whole log from its start makes.
func freshMemory(t *testing.T, o Options, id string) []memory.Entry {
	t.Helper()
	evs, err := log.ReadFile(log.SessionPath(o.Paths.SessionsDir, id))
	if err != nil && !errors.Is(err, log.ErrTruncated) {
		t.Fatal(err)
	}
	f := memory.Folder{Session: id}
	var out []memory.Entry
	at := map[string]int{}
	for _, ev := range evs {
		for _, e := range f.Observe(ev) {
			if i, ok := at[e.ID]; ok {
				out[i] = e
				continue
			}
			at[e.ID] = len(out)
			out = append(out, e)
		}
	}
	return out
}

// Memory search ranks this project's earlier turns and compaction notes by
// the words they share with the query (ADR 51): the card of the turn that
// fixed TestPagerOrder comes first for a question about it, a compaction's
// note is found as an episode, scope task keeps to one task, and another
// project's task, which says the same, is never found.
func TestMemorySearchFindsThisProjectsEarlierWork(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	project := config.CanonicalPath(o.Repo)
	longLog(t, o, "20261001T070000Z-0000aa01", 30, 25, pagerTurn)
	longLog(t, o, "20261002T070000Z-0000aa02", 5, 100, nil)
	other := o
	other.Repo = t.TempDir()
	longLog(t, other, "20261003T070000Z-0000bb01", 3, 100, pagerTurn)
	st := Store{Dir: o.Paths.SessionsDir}

	hits, err := st.SearchMemory(project, "", "why is TestPagerOrder flaky", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Session != "20261001T070000Z-0000aa01" || hits[0].Level != memory.LevelTurn || !strings.Contains(hits[0].Text, "wrote   pager/pager.go") {
		t.Fatalf("the pager turn is not first: %+v", hits)
	}
	if hits[0].Task != "run 0: look at last night's failures" {
		t.Fatalf("its task is %q", hits[0].Task)
	}
	for _, h := range hits {
		if h.Session == "20261003T070000Z-0000bb01" {
			t.Fatalf("another project's task was found: %+v", h)
		}
	}
	notes, err := st.SearchMemory(project, "", "the note: what the runs so far did", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) == 0 || notes[0].Level != memory.LevelEpisode {
		t.Fatalf("the compaction's note is not found first: %+v", notes)
	}
	if only, err := st.SearchMemory(project, "20261002T070000Z-0000aa02", "TestPagerOrder", 8); err != nil || len(only) != 0 {
		t.Fatalf("scope task found another task's turn: %+v, %v", only, err)
	}
}

// A session's memory is kept in <id>.mem and folded from where it ended:
// what it holds as the log grows, an unfinished turn included, is what
// folding the whole log gives; it covers only whole turns; and one that
// does not match its log, or is gone, is made again; deleting the session
// deletes it.
func TestMemoryIsFoldedFromTheEndAndChecked(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	id := "20261001T070000Z-0000cc01"
	longLog(t, o, id, 3, 100, func(write func(log.Kind, any)) {
		write(log.KindUserMsg, log.UserMsg{Text: "an unfinished turn"})
		write(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: "c", Name: "fs-list", Args: json.RawMessage(`{}`)}}})
	})
	st := Store{Dir: o.Paths.SessionsDir}
	same := func(when string) {
		t.Helper()
		forgetMemory(st, id)
		got, err := st.Memory(id)
		if err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		if a, b := asJSON(t, got), asJSON(t, freshMemory(t, o, id)); a != b {
			t.Fatalf("%s: memory\n%s\nis not the whole log's\n%s", when, a, b)
		}
	}
	same("with a turn under way")
	var m memFile
	raw, err := os.ReadFile(memPath(st.Dir, id))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Seq != 17 {
		t.Fatalf("the memory covers through seq %d; the last whole turn ends at 17", m.Seq)
	}
	appendEvents(t, o, id, func(write func(log.Kind, any)) {
		write(log.KindTurnEnd, log.TurnEnd{Outcome: "stopped"})
		pagerTurn(write)
	})
	same("after the log grew")
	got, _ := st.Memory(id)
	seen := map[string]bool{}
	for _, e := range got {
		if seen[e.ID] {
			t.Fatalf("entry %s twice", e.ID)
		}
		seen[e.ID] = true
	}
	if len(got) != 5 {
		t.Fatalf("%d entries, want 5 turns", len(got))
	}
	m.Hash = strings.Repeat("0", 64)
	if raw, err = json.Marshal(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memPath(st.Dir, id), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	same("with a memory file that is not this log's")
	if err := os.Remove(memPath(st.Dir, id)); err != nil {
		t.Fatal(err)
	}
	same("with the memory file deleted")
	if err := st.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(memPath(st.Dir, id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the memory file outlived its session: %v", err)
	}
}

// forgetMemory drops the in-memory copy of a session's memory, so the next
// read goes to its file.
func forgetMemory(st Store, id string) {
	memCache.mu.Lock()
	delete(memCache.entries, log.SessionPath(st.Dir, id))
	memCache.mu.Unlock()
}

// The exact search finds words as written, in any case, in tool output no
// card kept, newest first, across a log long enough to be read back mark
// by mark, each place with the card of its turn; never in another
// project's tasks.
func TestMemoryScanFindsWordsAsWrittenNewestFirst(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	project := config.CanonicalPath(o.Repo)
	id := "20261001T070000Z-0000dd01"
	longLog(t, o, id, 150, 160, pagerTurn)
	appendEvents(t, o, id, func(write func(log.Kind, any)) {
		for range 120 {
			write(log.KindUserMsg, log.UserMsg{Text: "another run"})
			write(log.KindLLMResp, log.LLMResp{Content: "nothing new"})
			write(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
		}
		pagerTurn(write)
	})
	other := o
	other.Repo = t.TempDir()
	longLog(t, other, "20261003T070000Z-0000dd02", 3, 100, pagerTurn)
	st := Store{Dir: o.Paths.SessionsDir}
	hits, err := st.ScanMemory(project, "", "CONNECTION reset by peer", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Seq <= hits[1].Seq || hits[1].Seq > 1000 || hits[0].Seq < 1100 {
		t.Fatalf("the two places, newest first: %+v", hits)
	}
	for _, h := range hits {
		if h.Session != id || h.Kind != log.KindToolResult || !strings.Contains(h.Snippet, "connection reset by peer") || h.Turn == "" {
			t.Fatalf("a place found: %+v", h)
		}
		if _, from, through, _, err := memory.ParseID(h.Turn); err != nil || h.Seq < from || h.Seq > through {
			t.Fatalf("its turn %s does not hold seq %d", h.Turn, h.Seq)
		}
	}
	if one, err := st.ScanMemory(project, "", "connection reset", 1); err != nil || len(one) != 1 || one[0].Seq != hits[0].Seq {
		t.Fatalf("at most one, the newest: %+v, %v", one, err)
	}
}

// memory-read shows a turn's card and its events from the log, an
// episode's note and the turns it covers, and refuses an entry of another
// project's task or one that is not there.
func TestMemoryReadShowsATurnAndAnEpisode(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	project := config.CanonicalPath(o.Repo)
	id := "20261001T070000Z-0000ee01"
	longLog(t, o, id, 6, 4, pagerTurn)
	other := o
	other.Repo = t.TempDir()
	longLog(t, other, "20261003T070000Z-0000ee02", 1, 100, nil)
	st := Store{Dir: o.Paths.SessionsDir}
	entries, err := st.Memory(id)
	if err != nil {
		t.Fatal(err)
	}
	var turn, episode memory.Entry
	for _, e := range entries {
		if e.Level == memory.LevelEpisode {
			episode = e
		}
		if strings.Contains(e.Text, "TestPagerOrder") {
			turn = e
		}
	}
	text, err := st.ReadMemory(project, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"run 0: look at last night's failures · turn", "asked   Fix the flaky TestPagerOrder", "person: Fix the flaky TestPagerOrder", `call shell-exec {"args":["test","./pager/"],"cmd":"go"}`, "shell-exec: ((\"status\" . 1)", "model: Sorted the keys", "turn ended: completed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the turn read does not show %q:\n%s", want, text)
		}
	}
	text, err = st.ReadMemory(project, episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "the note: what the runs so far did") || strings.Count(text, ":1  run ") != 4 {
		t.Fatalf("the episode read does not show its note and its four turns:\n%s", text)
	}
	foreign, err := st.Memory("20261003T070000Z-0000ee02")
	if err != nil || len(foreign) == 0 {
		t.Fatalf("the other project's memory: %v, %v", foreign, err)
	}
	if _, err := st.ReadMemory(project, foreign[0].ID); err == nil || !strings.Contains(err.Error(), "another repository") {
		t.Fatalf("another project's entry was read: %v", err)
	}
	if _, err := st.ReadMemory(project, fmt.Sprintf("m:%s:1-2:1", id)); err == nil {
		t.Fatal("an entry that is not there was read")
	}
}

// The tools in a session search this task's memory, and the memory of the
// repository's other tasks only while the person lets the model search
// earlier tasks for the project (ADR 50 §9): off, a search or a read of
// another task's entry is refused with how to turn it on, at once both ways.
func TestMemoryToolsInASession(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	longLog(t, o, "20261001T070000Z-0000ff01", 4, 100, pagerTurn)
	o.Provider = waitingProvider{}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	eval := func(src string) (string, error) {
		t.Helper()
		v, err := s.Eval(t.Context(), src)
		return v.String(), err
	}
	if got, err := eval(`(length (memory-search "TestPagerOrder flaky"))`); err != nil || got != "0" {
		t.Fatalf("this task's memory, which holds no turn yet: %v, %v", got, err)
	}
	for _, src := range []string{`(memory-search "TestPagerOrder flaky" "project")`, `(memory-search "connection reset by peer" "project" #t)`} {
		if _, err := eval(src); err == nil || !strings.Contains(err.Error(), "off for this project") {
			t.Fatalf("%s while earlier tasks are off: %v", src, err)
		}
	}
	allow := func(on bool) {
		t.Helper()
		if _, err := config.SaveRegistered(config.RegistryFile(o.Paths), config.Registered{Name: "code", Path: o.Repo, EarlierTasks: on}); err != nil {
			t.Fatal(err)
		}
	}
	allow(true)
	found, err := eval(`(assoc-ref (car (memory-search "TestPagerOrder flaky" "project")) 'id)`)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Trim(found, `"`)
	if !strings.HasPrefix(id, "m:20261001T070000Z-0000ff01:") {
		t.Fatalf("memory-search found %s", found)
	}
	if read, err := eval(fmt.Sprintf(`(memory-read %q)`, id)); err != nil || !strings.Contains(read, "person: Fix the flaky TestPagerOrder") {
		t.Fatalf("memory-read: %v, %v", read, err)
	}
	if exact, err := eval(`(length (memory-search "connection reset by peer" "project" #t))`); err != nil || exact != "1" {
		t.Fatalf("an exact search: %v, %v", exact, err)
	}
	allow(false)
	if _, err := eval(fmt.Sprintf(`(memory-read %q)`, id)); err == nil || !strings.Contains(err.Error(), "off for this project") {
		t.Fatalf("memory-read of another task while off: %v", err)
	}
	if _, err := eval(`(memory-search "pager" "everywhere")`); err == nil || !strings.Contains(err.Error(), `"task" or "project"`) {
		t.Fatalf("an unknown scope: %v", err)
	}
}
