package core

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
)

// taskLog writes a closed task's log in repo straight to the sessions
// directory: a start, a request, a read and its result holding text, then
// what extra adds. It returns the seq of the read's result.
func taskLog(t *testing.T, o Options, id, repo, text string, extra ...func(write func(log.Kind, any))) uint64 {
	t.Helper()
	if err := os.MkdirAll(o.Paths.SessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(log.SessionPath(o.Paths.SessionsDir, id))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var seq uint64
	write := func(kind log.Kind, payload any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		seq++
		line, _ := json.Marshal(log.Event{Seq: seq, TS: time.Date(2026, 10, 1, 9, 0, int(seq), 0, time.UTC), Session: id, Kind: kind, Payload: raw})
		if _, err := f.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	write(log.KindSessionStart, log.SessionStart{Repo: repo})
	write(log.KindUserMsg, log.UserMsg{Text: "look into " + text})
	write(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: "r", Name: "fs-read", Args: json.RawMessage(`{"path":"notes.txt"}`)}}})
	write(log.KindToolResult, log.ToolResult{ID: "r", Name: "fs-read", OK: true, Value: "first line\n" + text + "\nlast line"})
	result := seq
	for _, add := range extra {
		add(write)
	}
	write(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
	return result
}

// With the person's leave for the project, index-search ranks the events
// of the project's other tasks, archived ones marked, never another
// project's or its own, each under its turn after its task; recall brings
// one back by its task; a log that grew is read again, a deleted task is
// gone, and turned off it is refused again (ADR 50 §9, ADR 51).
func TestEarlierTasksAreSearchedWhenAllowed(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	o.Provider = waitingProvider{}
	other := t.TempDir()
	const kept, archived, elsewhere = "20261001T090000Z-0000aaaa", "20261002T090000Z-0000bbbb", "20261003T090000Z-0000cccc"
	keptSeq := taskLog(t, o, kept, o.Repo, "the gizmo calibration drifted")
	taskLog(t, o, archived, o.Repo, "the gizmo housing cracked", func(write func(log.Kind, any)) {
		write(log.KindSessionArchive, log.SessionArchive{Archived: true})
	})
	taskLog(t, o, elsewhere, other, "the gizmo of another project")
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	eval := func(src string) (string, error) {
		v, err := s.Eval(t.Context(), src)
		return v.S, err
	}
	if _, err := eval(`(index-search "gizmo" "earlier")`); err == nil || !strings.Contains(err.Error(), "off for this project") {
		t.Fatalf("off unless turned on: %v", err)
	}
	if _, err := eval(fmt.Sprintf(`(recall %d 0 0 %q)`, keptSeq, kept)); err == nil || !strings.Contains(err.Error(), "off for this project") {
		t.Fatalf("recall of an earlier task while off: %v", err)
	}
	registry := config.RegistryFile(o.Paths)
	allow := func(on bool) {
		t.Helper()
		if _, err := config.SaveRegistered(registry, config.Registered{Name: "code", Path: o.Repo, EarlierTasks: on}); err != nil {
			t.Fatal(err)
		}
	}
	allow(true)
	got, err := eval(`(index-search "gizmo calibration" "earlier")`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "[the 4 best of ") || !strings.Contains(got, "in this project's 2 earlier tasks") {
		t.Fatalf("the header: %q", got)
	}
	first := fmt.Sprintf(`]
task %s, "look into the gizmo calibration drifted" (2026-10-01), turn seq 2-5 · completed · 3s · asked "look into the gizmo calibration drifted"
  seq 2, the person's message
    1: look into the gizmo calibration drifted
  seq %d, the result of fs-read {"path":"notes.txt"}
    2: the gizmo calibration drifted
`, kept, keptSeq)
	if !strings.Contains(got, first) {
		t.Fatalf("the kept task's result, first: %q", got)
	}
	if !strings.Contains(got, archived) || !strings.Contains(got, ", archived)") {
		t.Fatalf("the archived task, marked: %q", got)
	}
	if strings.Contains(got, elsewhere) || strings.Contains(got, s.ID()) {
		t.Fatalf("another project's task or this one: %q", got)
	}
	back, err := eval(fmt.Sprintf(`(recall %d 2 1 %q)`, keptSeq, kept))
	if err != nil || back != fmt.Sprintf("[task %s, \"look into the gizmo calibration drifted\" (2026-10-01): seq %d, the result of fs-read {\"path\":\"notes.txt\"}]\n[lines 2-2 of 3]\nthe gizmo calibration drifted\n", kept, keptSeq) {
		t.Fatalf("recall from the earlier task: %q, %v", back, err)
	}
	if _, err := eval(fmt.Sprintf(`(recall 4 0 0 %q)`, elsewhere)); err == nil || !strings.Contains(err.Error(), "not one of this project's") {
		t.Fatalf("recall from another project's task: %v", err)
	}

	// A log that grew is read again.
	f, err := os.OpenFile(log.SessionPath(o.Paths.SessionsDir, kept), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(log.Event{Seq: 6, TS: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC), Session: kept, Kind: log.KindUserMsg, Payload: json.RawMessage(`{"text":"and the sprocket"}`)})
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got, err := eval(`(index-search "sprocket" "earlier")`); err != nil || !strings.Contains(got, "seq 6, the person's message") {
		t.Fatalf("a grown log: %q, %v", got, err)
	}

	// A deleted task is gone; turned off, the search is refused again.
	if err := (Store{Dir: o.Paths.SessionsDir}).Delete(archived); err != nil {
		t.Fatal(err)
	}
	if got, err := eval(`(index-search "housing cracked" "earlier")`); err != nil || strings.Contains(got, archived) {
		t.Fatalf("a deleted task: %q, %v", got, err)
	}
	allow(false)
	if _, err := eval(`(index-search "gizmo" "earlier")`); err == nil || !strings.Contains(err.Error(), "off for this project") {
		t.Fatalf("turned off: %v", err)
	}
}
