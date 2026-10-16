package core

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
)

func TestConversationSearchGroupsAndRestrictsTheProject(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	project := t.TempDir()
	add := func(id, repo string, archived bool) {
		l := newConversationLog(t, st.Dir, id, repo)
		l.add(log.KindSessionTitle, log.SessionTitle{Title: "<script>" + id + "</script>"})
		for range 2 {
			l.add(log.KindUserMsg, log.UserMsg{Text: "Find the starling lantern"})
			l.add(log.KindLLMResp, log.LLMResp{Content: strings.Repeat("界", 1000) + " starling lantern sk-ant-abcdefgh1234"})
			l.add(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
		}
		if archived {
			l.add(log.KindSessionArchive, log.SessionArchive{Archived: true})
		}
	}
	add("anchor", project, false)
	add("earlier", project, true)
	add("unrelated", t.TempDir(), false)
	current, err := st.SearchConversations("anchor", "conversation", "starling lantern", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Tasks) != 1 || current.Tasks[0].ID != "anchor" || len(current.Tasks[0].Turns) != 2 {
		t.Fatalf("%+v", current)
	}
	result, err := st.SearchConversations("anchor", "earlier", "starling lantern", 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.SearchedTasks != 1 || len(result.Tasks) != 1 || result.Tasks[0].ID != "earlier" || !result.Tasks[0].Archived {
		t.Fatalf("%+v", result)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-ant-") || !strings.Contains(string(raw), "starling lantern") {
		t.Fatalf("unsafe or missing snippets: %s", raw)
	}
	for _, turn := range result.Tasks[0].Turns {
		if !strings.Contains(turn.Heading, "completed") {
			t.Fatal(turn.Heading)
		}
		if len(turn.Matches) != 2 || turn.Matches[0].Seq >= turn.Matches[1].Seq {
			t.Fatalf("%+v", turn)
		}
		line := turn.Matches[1].Lines[0].Text
		if !strings.Contains(line, "starling lantern") || len([]rune(line)) > 202 {
			t.Fatal(line)
		}
	}
	limited, err := st.SearchConversations("anchor", "earlier", "starling", 1)
	if err != nil || len(limited.Tasks) != 1 || len(limited.Tasks[0].Turns) != 1 || len(limited.Tasks[0].Turns[0].Matches) != 1 {
		t.Fatalf("%+v %v", limited, err)
	}
	// An unreadable matched record must produce an incomplete-results signal,
	// not an apparently complete empty search.
	cached, err := st.memory("earlier", nil)
	if err != nil || cached.last == 0 {
		t.Fatalf("%v", err)
	}
	view, _, err := st.index("earlier")
	if err != nil || len(view.Turns) == 0 {
		t.Fatalf("%v", err)
	}
	path := log.SessionPath(st.Dir, "earlier")
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteAt([]byte("x"), view.Turns[0].Offset); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	partial, err := st.SearchConversations("anchor", "earlier", "starling lantern", 20)
	if err != nil || partial.Unavailable == 0 {
		t.Fatalf("%+v %v", partial, err)
	}
	for _, p := range []struct {
		id, scope, q string
		n            int
	}{{"anchor", "repo", "starling", 1}, {"anchor", "conversation", "---", 1}, {"anchor", "conversation", strings.Repeat("x", 4097), 1}, {"anchor", "conversation", "starling", 21}, {"../anchor", "conversation", "starling", 1}} {
		if _, err := st.SearchConversations(p.id, p.scope, p.q, p.n); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
	empty, err := st.SearchConversations("anchor", "conversation", "no-such-term", 20)
	if err != nil || len(empty.Tasks) != 0 || empty.SearchedEvents == 0 {
		t.Fatalf("%+v %v", empty, err)
	}
	// A search folds new work rather than relying on an old process cache.
	l := newConversationLog(t, st.Dir, "latest", project)
	l.add(log.KindUserMsg, log.UserMsg{Text: "freshneedle"})
	fresh, err := st.SearchConversations("anchor", "earlier", "freshneedle", 20)
	if err != nil || len(fresh.Tasks) != 1 || fresh.Tasks[0].ID != "latest" {
		t.Fatalf("%+v %v", fresh, err)
	}
}

func TestContextReadsOnlyTheMatchedTurnAndPagesBothWays(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	id := "context-range"
	l := newSyntheticLog(t, st.Dir, id)
	l.add(log.KindScheduleRun, log.ScheduleRun{})
	l.turns(40)
	run := lastSeq(t, log.SessionPath(st.Dir, id)) + 1
	l.add(log.KindScheduleRun, log.ScheduleRun{})
	start := lastSeq(t, log.SessionPath(st.Dir, id)) + 1
	l.add(log.KindUserMsg, log.UserMsg{Text: "current turn"})
	for range 600 {
		l.add(log.KindNote, log.Note{Text: "padding"})
	}
	selected := lastSeq(t, log.SessionPath(st.Dir, id)) + 1
	l.add(log.KindLLMResp, log.LLMResp{Content: "matching conversation"})
	for range 100 {
		l.add(log.KindNote, log.Note{Text: "later padding"})
	}
	l.add(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
	end := lastSeq(t, log.SessionPath(st.Dir, id))
	l.add(log.KindUserMsg, log.UserMsg{Text: "later turn"})
	l.add(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
	before, err := st.ReadContext(id, selected)
	if err != nil {
		t.Fatal(err)
	}
	if before.From != start || before.Through != end || before.Run.Seq != run || before.Turn == nil || before.Turn.Seq != start || len(before.Page.Events) != 81 || before.Page.Events[40].Seq != selected {
		t.Fatalf("%+v", before)
	}
	first, last := before.Page.Events[0].Seq, before.Page.Next
	earlier, err := st.ReadLog(id, max(start-1, first-501), first-1, 500)
	if err != nil || len(earlier.Events) != 500 {
		t.Fatalf("%+v %v", earlier, err)
	}
	later, err := st.ReadLog(id, last, end, 500)
	if err != nil || later.Next != end {
		t.Fatalf("%+v %v", later, err)
	}
	// Cached offsets must be enough even when a former run cannot be read.
	idx, _, err := st.index(id)
	if err != nil {
		t.Fatal(err)
	}
	offset := int64(0)
	for _, mark := range idx.Turns {
		if mark.Seq == start {
			offset = mark.Offset
		}
	}
	f, err := os.OpenFile(log.SessionPath(st.Dir, id), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteAt([]byte(strings.Repeat("x", int(offset))), 0); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := st.ReadContext(id, selected)
	if err != nil {
		t.Fatal(err)
	}
	if asJSON(t, before) != asJSON(t, after) {
		t.Fatal("context depended on the unreadable old run")
	}
	for _, seq := range []uint64{0, end + 1000} {
		if _, err = st.ReadContext(id, seq); err == nil {
			t.Fatal("accepted", seq)
		}
	}
}

func newConversationLog(t *testing.T, dir, id, project string) *syntheticLog {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(log.SessionPath(dir, id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	l := &syntheticLog{t: t, f: f, w: log.New(struct{ io.Writer }{f}, id), repo: project}
	path := filepath.Join(project, ".nodwyn", "worktrees", id)
	l.add(log.KindSessionStart, log.SessionStart{Repo: path, Worktree: &log.Worktree{Repository: project, Path: path, Branch: "nodwyn/" + id}})
	return l
}
