package core

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/schedule"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestScheduledRunsResetSpendAndResume(t *testing.T) {
	srv := fake.NewT(t, fake.Turn{Content: "first"}, fake.Turn{Content: "second"})
	defer srv.Close()
	o := diskOptions(t)
	o.NoConfirm = false
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	d := log.SessionSchedule{Definition: schedule.Definition{Message: "check", Repeat: "daily", At: "2026-10-16T09:00", Cost: 2}}
	if _, err = s.Record(log.KindSessionSchedule, d); err != nil {
		t.Fatal(err)
	}
	cost := 1.5
	if _, err = s.Record(log.KindLLMResp, log.LLMResp{Usage: log.Usage{Cost: &cost}}); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err = s.SendScheduled("check", log.ScheduleRun{Due: time.Now(), Started: time.Now(), Cost: 2, Manual: true}); err != nil {
			t.Fatal(err)
		}
		followBudget(t, s, func(event.Checkpoint) { t.Fatal("earlier run's spend carried over") })
		got, ok := s.RunSpend()
		if !ok || got != 0 {
			t.Fatalf("run spend %v %v", got, ok)
		}
		if _, err = s.Record(log.KindLLMResp, log.LLMResp{Usage: log.Usage{Cost: &cost}}); err != nil {
			t.Fatal(err)
		}
		id := s.ID()
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = Resume(o, id)
		if err != nil {
			t.Fatal(err)
		}
		got, ok = s.RunSpend()
		if !ok || math.Abs(got-1.5) > 1e-8 {
			t.Fatalf("resumed spend %v %v", got, ok)
		}
	}
	defer s.Close()
	state, err := (Store{Dir: o.Paths.SessionsDir}).Schedule(s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.Definition == nil || state.LastRun == nil || !state.Cursor.IsZero() {
		t.Fatalf("%+v", state)
	}
	totals, err := (Store{Dir: o.Paths.SessionsDir}).Totals(s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if totals.Runs != 2 {
		t.Fatalf("run boundaries: %d", totals.Runs)
	}
}

func TestScheduledHandoverWaitsForNextRun(t *testing.T) {
	srv := fake.NewT(t, fake.Turn{Content: "first answer"}, fake.Turn{Content: "Carry the release number 42."}, fake.Turn{Content: "human answer"}, fake.Turn{Content: "next run answer"})
	defer srv.Close()
	o := diskOptions(t)
	o.NoConfirm = false
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	def := log.SessionSchedule{Definition: schedule.Definition{Message: "check release", Repeat: "daily", At: "2026-10-16T09:00"}}
	if _, err = s.Record(log.KindSessionSchedule, def); err != nil {
		t.Fatal(err)
	}
	bulk := "Earlier release evidence " + strings.Repeat("z", 50000)
	if _, err = s.Record(log.KindUserMsg, log.UserMsg{Text: bulk}); err != nil {
		t.Fatal(err)
	}
	if err = s.SendScheduled(def.Message, log.ScheduleRun{Manual: true}); err != nil {
		t.Fatal(err)
	}
	followBudget(t, s, func(event.Checkpoint) { t.Fatal("checkpoint") })
	note := s.handover.Load()
	if note == nil || !strings.Contains(note.Text, "42") {
		t.Fatalf("handover: %+v", note)
	}
	for _, ev := range s.EventsSoFar() {
		if ev.Kind == log.KindContextNarrow {
			t.Fatal("handover applied before next run")
		}
	}
	// The note is for the next run: the agent's last answer is still its own.
	if reply, ok := s.FinalReply(); !ok || reply.Content != "first answer" {
		t.Fatalf("the last answer after the handover: %q", reply.Content)
	}
	if err = s.Send("What was that evidence?"); err != nil {
		t.Fatal(err)
	}
	followBudget(t, s, func(event.Checkpoint) { t.Fatal("checkpoint") })
	if err = s.SendScheduled(def.Message, log.ScheduleRun{Manual: true}); err != nil {
		t.Fatal(err)
	}
	followBudget(t, s, func(event.Checkpoint) { t.Fatal("checkpoint") })
	// Reading the fake's requests is safe after the final idle event.
	if len(srv.Requests) != 4 {
		t.Fatalf("%d requests", len(srv.Requests))
	}
	if !strings.Contains(string(srv.Requests[2].Raw), "Earlier release evidence") {
		t.Fatal("human follow-up lost the whole conversation")
	}
	// The ask for the note and the note stay out of the conversation.
	if follow := string(srv.Requests[2].Raw); strings.Contains(follow, "Carry the release number 42") || strings.Contains(follow, "handover note") {
		t.Fatal("the human follow-up carries the handover's exchange")
	}
	// The follow-up is in no note, so the next run keeps the whole
	// conversation instead of narrowing to a note that lacks it.
	last := string(srv.Requests[3].Raw)
	if !strings.Contains(last, "Earlier release evidence") || !strings.Contains(last, "What was that evidence?") {
		t.Fatal("next run dropped the follow-up that no note covers")
	}
}

// A scheduled task whose context is large has a handover to prepare after a
// conversation; an evaluation, which has none, ends without a model request
// (P2-039). The fake has no turn to give, so a request fails the test.
func TestAnEvaluationOnAScheduledTaskPreparesNoHandover(t *testing.T) {
	srv := fake.NewT(t)
	defer srv.Close()
	o := diskOptions(t)
	o.NoConfirm = false
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	def := log.SessionSchedule{Definition: schedule.Definition{Message: "check release", Repeat: "daily", At: "2026-10-16T09:00"}}
	if _, err = s.Record(log.KindSessionSchedule, def); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Record(log.KindUserMsg, log.UserMsg{Text: "Earlier release evidence " + strings.Repeat("z", 50000)}); err != nil {
		t.Fatal(err)
	}
	if err = s.StartEval("(+ 1 2)", "test"); err != nil {
		t.Fatal(err)
	}
	followBudget(t, s, func(event.Checkpoint) { t.Fatal("checkpoint") })
	if len(srv.Requests) != 0 || s.handover.Load() != nil {
		t.Fatalf("the evaluation asked the model: %d requests, handover %+v", len(srv.Requests), s.handover.Load())
	}
}

// A run that follows a note is in no note and, until a later one covers it,
// the next run must still see it: narrowing to the old note dropped small
// runs from the model's memory for good (P2-050). The first run after the
// note does start from it, as nothing was written after the note.
func TestScheduledSmallRunsSurviveTheNextRun(t *testing.T) {
	srv := fake.NewT(t, fake.Turn{Content: "first answer"}, fake.Turn{Content: "Carry the release number 42."}, fake.Turn{Content: "yesterday found gadget 7"}, fake.Turn{Content: "third answer"})
	defer srv.Close()
	o := diskOptions(t)
	o.NoConfirm = false
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	def := log.SessionSchedule{Definition: schedule.Definition{Message: "check release", Repeat: "daily", At: "2026-10-16T09:00"}}
	if _, err = s.Record(log.KindSessionSchedule, def); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Record(log.KindUserMsg, log.UserMsg{Text: "Earlier release evidence " + strings.Repeat("z", 50000)}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err = s.SendScheduled(def.Message, log.ScheduleRun{Manual: true}); err != nil {
			t.Fatal(err)
		}
		followBudget(t, s, func(event.Checkpoint) { t.Fatal("checkpoint") })
	}
	if len(srv.Requests) != 4 {
		t.Fatalf("%d requests", len(srv.Requests))
	}
	if second := string(srv.Requests[2].Raw); strings.Contains(second, "Earlier release evidence") || !strings.Contains(second, "Carry the release number 42") {
		t.Fatal("the run after the note did not start from it")
	}
	if third := string(srv.Requests[3].Raw); !strings.Contains(third, "yesterday found gadget 7") {
		t.Fatal("the third run lost what the second found")
	}
}

func TestScheduledRunDiscardsDontAskAndRequiresApproval(t *testing.T) {
	srv := fake.NewT(t, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "write", Name: "fs-write", Args: map[string]any{"path": "scheduled.txt", "content": "scheduled"}}}}, fake.Turn{Content: "written"})
	defer srv.Close()
	o := diskOptions(t)
	o.NoConfirm = false
	o.Config.Approval.DontAsk = true
	o.Config.Approval.Writes = config.ApproveAsk
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SetApproveAll(true, "test"); err != nil {
		t.Fatal(err)
	}
	if err = s.SendScheduled("write", log.ScheduleRun{Manual: true}); err != nil {
		t.Fatal(err)
	}
	events := followBudget(t, s, func(event.Checkpoint) { t.Fatal("checkpoint") })
	confirms := 0
	for _, ev := range events {
		if ev.Kind == event.PromptConfirm {
			confirms++
		}
	}
	if confirms != 1 {
		t.Fatalf("scheduled write asked %d times", confirms)
	}
	all, _ := s.ApproveAll()
	if all {
		t.Fatal("scheduled run retained Don't ask")
	}
}

// A scheduled run starts from where its base is now (ADR 49 §3). Close
// removes a worktree that holds nothing, but one a run left ignored files
// in stays, and every later run worked on the first run's base. A worktree
// with no work in it is brought up to its base, its ignored files kept.
func TestScheduledRunStartsFromTodaysBase(t *testing.T) {
	repo, git := gitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("__pycache__/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", ".gitignore")
	git(repo, "commit", "-q", "-m", "ignore caches")
	o := worktreeOptions(t, repo, fake.Turn{Content: "run answer"})
	o.NoConfirm = false
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := s.Worktree()
	if err := os.MkdirAll(filepath.Join(w.Path, "__pycache__"), 0o755); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(w.Path, "__pycache__", "x.pyc")
	if err := os.WriteFile(cache, []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "today.txt"), []byte("today\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", "today.txt")
	git(repo, "commit", "-q", "-m", "today")
	def := log.SessionSchedule{Definition: schedule.Definition{Message: "run the tests", Repeat: "daily", At: "2026-10-16T09:00"}}
	if _, err = s.Record(log.KindSessionSchedule, def); err != nil {
		t.Fatal(err)
	}
	if err = s.SendScheduled(def.Message, log.ScheduleRun{Manual: true}); err != nil {
		t.Fatal(err)
	}
	followBudget(t, s, func(event.Checkpoint) { t.Fatal("checkpoint") })
	if got, want := git(w.Path, "rev-parse", "HEAD"), git(repo, "rev-parse", "main"); got != want {
		t.Fatalf("the run started at %s, not at main's %s", got, want)
	}
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("the ignored file went: %v", err)
	}
}

// A task's schedule is answered from memory while its log is unchanged, as
// its listing is: reading it decoded the task's whole index every time, which
// a list of a few hundred tasks paid for under the daemon's lock (P2-074).
func TestScheduleIsReadOnceWhileTheLogIsUnchanged(t *testing.T) {
	o := diskOptions(t)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	d := log.SessionSchedule{Definition: schedule.Definition{Message: "check", Repeat: "daily", At: "2026-10-16T09:00"}}
	if _, err = s.Record(log.KindSessionSchedule, d); err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	store := Store{Dir: o.Paths.SessionsDir}
	first, err := store.Schedule(id)
	if err != nil || first.Definition == nil {
		t.Fatalf("%+v %v", first, err)
	}
	// The index is what a read would decode: without it, only memory can answer.
	if err := os.Remove(indexPath(o.Paths.SessionsDir, id)); err != nil {
		t.Fatal(err)
	}
	second, err := store.Schedule(id)
	if err != nil || second.Definition == nil || second.Sequence != first.Sequence {
		t.Fatalf("a second read: %+v %v", second, err)
	}
	if _, err := os.Stat(indexPath(o.Paths.SessionsDir, id)); err == nil {
		t.Fatal("the index was decoded and written again for a log that had not changed")
	}
	// A reader's change to what it was given does not reach the next one.
	second.Definition.Message = "changed"
	if third, _ := store.Schedule(id); third.Definition.Message != "check" {
		t.Fatalf("a reader changed the cached schedule: %q", third.Definition.Message)
	}
}
