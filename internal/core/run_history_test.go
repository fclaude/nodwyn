package core

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
)

func TestRunHistoryPagesAreFixedAndRedacted(t *testing.T) {
	t.Parallel()
	st := Store{Dir: t.TempDir()}
	id := "run-history"
	l := newSyntheticLog(t, st.Dir, id)
	l.add(log.KindSessionModel, log.SessionModel{Model: "chosen"})
	starts := []uint64{}
	for i := 0; i < 8; i++ {
		starts = append(starts, lastSeq(t, log.SessionPath(st.Dir, id))+1)
		l.add(log.KindRunState, log.RunState{Status: "running", Turns: 0})
		l.add(log.KindRunTurn, log.RunTurn{Number: 1})
		l.add(log.KindUserMsg, log.UserMsg{Text: "request"})
		l.add(log.KindLLMResp, log.LLMResp{Content: strings.Repeat("界", 2500) + " sk-ant-abcdefgh1234"})
		l.add(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
	}
	latest, err := st.ReadLatestRun(id, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Run.Seq != starts[7] || !latest.Earlier || !latest.More || latest.Events[0].Seq != starts[7] {
		t.Fatalf("latest: %+v", latest)
	}
	if len(latest.Context) == 0 || latest.Context[len(latest.Context)-1].Kind != log.KindSessionModel {
		t.Fatalf("setup missing: %+v", latest.Context)
	}
	first, err := st.ReadRuns(id, latest.Run.Seq, latest.Through, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Runs) != 3 || !first.More || first.Before != starts[4] || first.Runs[0].Through != starts[7]-1 || len([]rune(first.Runs[0].Reply)) != 2001 {
		t.Fatalf("earlier: %+v", first)
	}
	// A new run cannot move the end or the cursor of pages already selected.
	l.add(log.KindRunState, log.RunState{Status: "running"})
	second, err := st.ReadRuns(id, first.Before, first.Through, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Runs) != 4 || second.More || second.Runs[0].Seq != starts[3] || second.Runs[3].Seq != 1 {
		t.Fatalf("old page moved: %+v", second)
	}
	tail, err := st.ReadLog(id, latest.Next, latest.Through, 100)
	if err != nil {
		t.Fatal(err)
	}
	if tail.Next != latest.Through || tail.More {
		t.Fatalf("run tail: %+v", tail)
	}
	for _, p := range []struct {
		before, through uint64
		limit           int
	}{{2, 0, 1}, {0, 0, 0}, {0, 0, 101}, {latest.Through + 20, 0, 1}, {0, latest.Through + 20, 1}} {
		if _, err := st.ReadRuns(id, p.before, p.through, p.limit); err == nil {
			t.Fatalf("accepted bad cursor: %+v", p)
		}
	}
	l.add(log.KindUserMsg, log.UserMsg{Text: "human followup"})
	l.add(log.KindLLMResp, log.LLMResp{Content: "last sk-ant-abcdefgh1234 reply"})
	now, err := st.ReadRuns(id, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(now.Runs[0].Reply, "sk-ant-") || !strings.Contains(now.Runs[0].Reply, "REDACTED") {
		t.Fatalf("preview not redacted: %+v", now)
	}
	l.add(log.KindLLMResp, log.LLMResp{Content: strings.Repeat("x", 1990) + " sk-ant-abcdefgh1234"})
	clipped, err := st.ReadRuns(id, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(clipped.Runs[0].Reply, "sk-ant-") {
		t.Fatalf("clipping exposed a partial legacy secret: %q", clipped.Runs[0].Reply)
	}
	now = clipped
	if err := os.Remove(indexPath(st.Dir, id)); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := st.ReadRuns(id, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if asJSON(t, now) != asJSON(t, rebuilt) {
		t.Fatal("rebuilding changed the history")
	}
}

func TestRunHistoryDoesNotReadTheOlderPrefix(t *testing.T) {
	t.Parallel()
	st := Store{Dir: t.TempDir()}
	id := "run-suffix"
	l := newSyntheticLog(t, st.Dir, id)
	for i := 0; i < 3; i++ {
		l.add(log.KindRunState, log.RunState{Status: "running"})
		l.turns(120)
	}
	before, err := st.ReadLatestRun(id, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(indexPath(st.Dir, id))
	if err != nil {
		t.Fatal(err)
	}
	var idx logIndex
	if err = json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	offset := idx.Runs[len(idx.Runs)-1].Offset
	f, err := os.OpenFile(log.SessionPath(st.Dir, id), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.WriteAt([]byte(strings.Repeat("x", int(offset))), 0); err != nil {
		t.Fatal(err)
	}
	after, err := st.ReadLatestRun(id, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if asJSON(t, before) != asJSON(t, after) {
		t.Fatal("latest run changed after prefix became unreadable")
	}
	page, err := st.ReadRuns(id, before.Run.Seq, before.Through, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 2 {
		t.Fatalf("previews read the prefix: %+v", page)
	}
	if _, err := st.ReadLog(id, 0, 0, 10); err == nil {
		t.Fatal("the old prefix is readable")
	}
}

func TestRunHistoryKeepsConversationBeforeTheFirstRun(t *testing.T) {
	t.Parallel()
	st := Store{Dir: t.TempDir()}
	id := "run-intro"
	l := newSyntheticLog(t, st.Dir, id)
	l.add(log.KindUserMsg, log.UserMsg{Text: "chat"})
	l.add(log.KindLLMResp, log.LLMResp{Content: "old reply"})
	chat, err := st.ReadLatestRun(id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if chat.Run.Seq != 1 || chat.Earlier || len(chat.Events) != 4 {
		t.Fatalf("ordinary conversation was cut: %+v", chat)
	}
	l.add(log.KindRunState, log.RunState{Status: "running"})
	run, err := st.ReadLatestRun(id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	intro, err := st.ReadRuns(id, run.Run.Seq, run.Through, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(intro.Runs) != 1 || intro.Runs[0].Reply != "old reply" || intro.Runs[0].Seq != 1 {
		t.Fatalf("old conversation missing: %+v", intro)
	}
	// Selecting a prefix before that run must not reveal its newer records.
	old, err := st.ReadLatestRun(id, chat.Through, 100)
	if err != nil {
		t.Fatal(err)
	}
	if old.Run.Seq != 1 || old.Through != chat.Through || len(old.Events) != 4 {
		t.Fatalf("fixed prefix: %+v", old)
	}
}

// A task that runs every day resumes before each run, and the resume
// records the whole setup (the system prompt, the tools' schemas): the
// index kept a copy of it in every run's mark, so it grew by that much a
// run (17 MB for a year of a daily task). Only the latest run's is kept
// whole; an earlier run's is read from the log when it is opened.
func TestRunSetupIsKeptOnceInTheIndex(t *testing.T) {
	t.Parallel()
	st := Store{Dir: t.TempDir()}
	id := "daily"
	l := newSyntheticLog(t, st.Dir, id)
	system := strings.Repeat("You are nodwyn. ", 2000) // 32 KB, as a real setup is
	for run := 0; run < 30; run++ {
		l.add(log.KindSessionResume, log.SessionStart{Repo: l.repo, Context: &log.WindowContext{System: system + strconv.Itoa(run)}})
		l.add(log.KindRunState, log.RunState{Status: "running"})
		l.turns(2)
	}
	latest, err := st.ReadLatestRun(id, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(indexPath(st.Dir, id))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 4*int64(len(system)) {
		t.Fatalf("the index is %d bytes for 30 runs of a %d-byte setup", info.Size(), len(system))
	}
	if !strings.Contains(asJSON(t, latest.Context), "nodwyn. 29") {
		t.Fatal("the latest run lost its setup")
	}
	// An earlier run, opened at its own end, reads its setup from the log.
	earlier, err := st.ReadLatestRun(id, latest.Run.Seq-1, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got := asJSON(t, earlier.Context); !strings.Contains(got, "nodwyn. 28") || strings.Contains(got, "nodwyn. 29") {
		t.Fatalf("an earlier run's setup: %.200s", got)
	}
}
