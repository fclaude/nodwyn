package core

import (
	"reflect"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/test/fakes/chat"
)

// chatSession is a conversation with the fake provider of effort_test.go,
// which a test can make hold every reply (srv.Hold) so that it decides when
// a turn ends.
func chatSession(t *testing.T) (*chat.Server, *Session) {
	t.Helper()
	srv := newChat()
	t.Cleanup(srv.Close)
	s, err := New(effortOptions(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return srv, s
}

// userTexts are the texts of the user.msg records so far, in order.
func userTexts(t *testing.T, s *Session) []string {
	t.Helper()
	var out []string
	for _, ev := range s.EventsSoFar() {
		if ev.Kind != log.KindUserMsg {
			continue
		}
		var m log.UserMsg
		if err := ev.Decode(&m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m.Text)
	}
	return out
}

// turnShape is the order of the user.msg and turn.end records, which says
// whether turns ran one after another or overlapped.
func turnShape(s *Session) string {
	var out []string
	for _, ev := range s.EventsSoFar() {
		if ev.Kind == log.KindUserMsg || ev.Kind == log.KindTurnEnd {
			out = append(out, string(ev.Kind))
		}
	}
	return strings.Join(out, " ")
}

// outcomes are how each turn so far ended.
func outcomes(t *testing.T, s *Session) []string {
	t.Helper()
	var out []string
	for _, ev := range s.EventsSoFar() {
		if ev.Kind != log.KindTurnEnd {
			continue
		}
		var end log.TurnEnd
		if err := ev.Decode(&end); err != nil {
			t.Fatal(err)
		}
		out = append(out, end.Outcome)
	}
	return out
}

// Messages queued behind a running turn run one after another once it has
// ended, in the order they were queued, each as its own turn, and the session
// is idle only after the last: a queued message that ran at once, ran
// reversed, ran beside the first or was dropped would show in the records.
func TestQueueWhileBusy(t *testing.T) {
	t.Parallel()
	srv, s := chatSession(t)
	srv.Hold()
	if err := s.Send("a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first request to be held", func() bool { return srv.Waiting() == 1 })
	for _, text := range []string{"b", "c"} {
		if err := s.Queue(text); err != nil {
			t.Fatal(err)
		}
	}
	if got := userTexts(t, s); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("a queued message ran while the turn did: %v", got)
	}
	if err := s.Send("plain"); err == nil {
		t.Fatal("a plain send was taken while a turn ran")
	}
	srv.Release()
	evs := (&rig{t: t, s: s}).drain("n")
	if got := userTexts(t, s); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("user messages %v, want a, b, c", got)
	}
	if got, want := turnShape(s), strings.TrimSpace(strings.Repeat("user.msg turn.end ", 3)); got != want {
		t.Fatalf("turns overlapped or were not all run:\n got %s\nwant %s", got, want)
	}
	// drain returns at the first idle, so a session that went idle between
	// the turns would have returned before the last one ended.
	ends := 0
	for _, ev := range evs {
		if ev.Kind == "turn.end" {
			ends++
		}
	}
	if ends != 3 || s.Busy() || len(srv.Requests()) != 3 {
		t.Fatalf("%d turn.end events before idle, busy %v, %d requests", ends, s.Busy(), len(srv.Requests()))
	}
}

// On an idle session a queued message is an ordinary send.
func TestQueueOnAnIdleSessionStartsATurn(t *testing.T) {
	t.Parallel()
	srv, s := chatSession(t)
	if err := s.Queue("x"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	if got := userTexts(t, s); !reflect.DeepEqual(got, []string{"x"}) || len(srv.Requests()) != 1 {
		t.Fatalf("user messages %v, %d requests", got, len(srv.Requests()))
	}
}

// Stop stops everything: a message queued behind the turn is discarded with
// it, and no turn starts after it.
func TestStopDiscardsQueuedMessages(t *testing.T) {
	t.Parallel()
	srv, s := chatSession(t)
	srv.Hold()
	if err := s.Send("a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the request to be held", func() bool { return srv.Waiting() == 1 })
	if err := s.Queue("b"); err != nil {
		t.Fatal(err)
	}
	s.Abort()
	(&rig{t: t, s: s}).drain("n")
	if got := userTexts(t, s); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("user messages %v: the queue ran after Stop", got)
	}
	if got := outcomes(t, s); !reflect.DeepEqual(got, []string{"canceled"}) || s.Busy() {
		t.Fatalf("outcomes %v, busy %v", got, s.Busy())
	}
}

// A steering message stops the turn that is running and is the next one,
// ahead of what was already queued, which still runs after it. On an idle
// session it is an ordinary send.
func TestSteerWhileBusy(t *testing.T) {
	t.Parallel()
	srv, s := chatSession(t)
	srv.Hold()
	if err := s.Send("first"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first request to be held", func() bool { return srv.Waiting() == 1 })
	if err := s.Queue("later"); err != nil {
		t.Fatal(err)
	}
	if err := s.Steer("now"); err != nil {
		t.Fatal(err)
	}
	// The first turn ends by being stopped, while its reply is still held,
	// and the steering message starts: a steer that only waited would leave
	// the first turn held until this times out.
	waitFor(t, "the steering message to start", func() bool { return len(userTexts(t, s)) == 2 })
	if got := userTexts(t, s); !reflect.DeepEqual(got, []string{"first", "now"}) {
		t.Fatalf("user messages %v, want first, now", got)
	}
	srv.Release()
	(&rig{t: t, s: s}).drain("n")
	if got := userTexts(t, s); !reflect.DeepEqual(got, []string{"first", "now", "later"}) {
		t.Fatalf("user messages %v, want first, now, later", got)
	}
	if got, want := outcomes(t, s), []string{"canceled", "completed", "completed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outcomes %v, want %v", got, want)
	}
}

func TestSteerOnAnIdleSessionStartsATurn(t *testing.T) {
	t.Parallel()
	srv, s := chatSession(t)
	if err := s.Steer("x"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	if got, want := outcomes(t, s), []string{"completed"}; !reflect.DeepEqual(got, want) || !reflect.DeepEqual(userTexts(t, s), []string{"x"}) || len(srv.Requests()) != 1 {
		t.Fatalf("outcomes %v, user messages %v", got, userTexts(t, s))
	}
}

// While a model switch holds the session, a queued or a steering message is
// refused as a plain one is, and is not kept to run later.
func TestQueueRefusedWhileSwitchingModel(t *testing.T) {
	t.Parallel()
	_, s := chatSession(t)
	s.mu.Lock()
	done := s.holdSwitching()
	s.mu.Unlock()
	for name, send := range map[string]func(string) error{"queue": s.Queue, "steer": s.Steer} {
		if err := send("x"); err == nil || !strings.Contains(err.Error(), "the model is being switched; send when it is done") {
			t.Fatalf("%s while switching: %v", name, err)
		}
	}
	done()
	s.mu.Lock()
	kept := len(s.sends)
	s.mu.Unlock()
	if kept != 0 {
		t.Fatalf("%d refused messages were kept", kept)
	}
}

// A model chosen while a turn runs is applied when it ends, and a message
// queued behind that turn runs on it: the turn for "b" is asked of the model
// that was chosen, not of the one it replaced.
func TestQueuedMessageRunsOnQueuedModel(t *testing.T) {
	t.Parallel()
	srv, s := chatSession(t)
	srv.Hold()
	if err := s.Send("a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the request to be held", func() bool { return srv.Waiting() == 1 })
	state, err := s.SetModel(ModelChoice{Name: "plain"})
	if err != nil || state.Queued == nil {
		t.Fatalf("the choice was not queued: %+v, %v", state, err)
	}
	if err := s.Queue("b"); err != nil {
		t.Fatal(err)
	}
	srv.Release()
	(&rig{t: t, s: s}).drain("n")
	var models []string
	for _, r := range srv.Requests() {
		models = append(models, r.Model)
	}
	if want := []string{"thinker", "plain"}; !reflect.DeepEqual(models, want) {
		t.Fatalf("the provider was asked of %v, want %v", models, want)
	}
	if got := s.Model(); got.Queued != nil || got.Notice != "" {
		t.Fatalf("after the turns: %+v", got)
	}
}
