package core

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A chat-completions server's reported cost and cache use reach llm.resp;
// without a report, configured prices give an estimate marked as one, and
// neither gives nothing.
func TestModelSpendReachesTheLog(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		prices      bool
		cost        *float64
		estimated   bool
		cachedKnown bool
	}{
		{"reported", `{"prompt_tokens":1000,"completion_tokens":100,"cost":0.004,"prompt_tokens_details":{"cached_tokens":800}}`, false, price(0.004), false, true},
		{"estimated", `{"prompt_tokens":1000,"completion_tokens":100}`, true, price(0.0045), true, false},
		{"unknown", `{"prompt_tokens":1000,"completion_tokens":100}`, false, nil, false, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":` + tc.usage + `}`))
		}))
		cfg := config.Default()
		cfg.Control = config.Model{Provider: "openai", URL: srv.URL + "/v1", Model: "m"}
		if tc.prices {
			cfg.Control.PriceInput, cfg.Control.PriceOutput = price(3), price(15)
		}
		logb := &bytes.Buffer{}
		s, err := New(Options{Config: cfg, Repo: t.TempDir(), Provider: llm.NewOpenAI(srv.URL+"/v1", nil), NoConfirm: true, LogWriter: logb})
		if err != nil {
			t.Fatal(err)
		}
		r := &rig{t: t, s: s, logb: logb}
		if e := r.turn("hello"); e != "" {
			t.Fatal(e)
		}
		s.Close()
		srv.Close()
		var u log.Usage
		for _, ev := range r.logged() {
			if ev.Kind == log.KindLLMResp {
				var p log.LLMResp
				ev.Decode(&p)
				u = p.Usage
			}
		}
		if (u.Cost == nil) != (tc.cost == nil) || (u.Cost != nil && (*u.Cost-*tc.cost > 1e-12 || *tc.cost-*u.Cost > 1e-12)) || u.CostEstimated != tc.estimated || (u.CachedTokens != nil) != tc.cachedKnown {
			t.Fatalf("%s: usage %+v", tc.name, u)
		}
	}
}

func price(f float64) *float64 { return &f }

// spendRig opens a session on disk whose model costs $0.50 a request (its
// output priced so), with a spend limit of soft..hard dollars.
func spendRig(t *testing.T, soft, hard float64, priced bool, turns ...fake.Turn) (*Session, Options, *fake.Server) {
	t.Helper()
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	o := diskOptions(t)
	o.NoConfirm = false
	o.Provider = llm.NewOllama(srv.URL)
	o.Config.Budget.CostSoft, o.Config.Budget.CostHard = soft, hard
	if priced {
		// The fake reports 44 output tokens a request.
		o.Config.Control.PriceInput, o.Config.Control.PriceOutput = price(0), price(0.5e6/44)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, o, srv
}

func grep(id, pattern string) fake.Turn {
	return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "index-grep", Args: map[string]any{"path": ".", "pattern": pattern}}}}
}

// spendOf reads what the log says: requests made, checkpoints asked about
// spend, the last turn's outcome, and the notes.
func spendOf(s *Session) (requests, checkpoints int, outcome string, notes []string) {
	for _, ev := range s.EventsSoFar() {
		switch ev.Kind {
		case log.KindLLMResp:
			requests++
		case log.KindTurnCheckpoint:
			var p log.TurnCheckpoint
			if ev.Decode(&p) == nil && p.Checkpoint.Dimension == "cost" {
				checkpoints++
			}
		case log.KindTurnEnd:
			var p log.TurnEnd
			ev.Decode(&p)
			outcome = p.Outcome
		case log.KindNote:
			var p log.Note
			ev.Decode(&p)
			notes = append(notes, p.Text)
		}
	}
	return
}

// The spend limit asks at its soft limit, as often as it is raised, and
// stops at the hard one; the next turn starts past it and stops before it
// asks the model anything.
func TestSpendLimitAsksThenStopsTheTask(t *testing.T) {
	s, _, _ := spendRig(t, 1, 2, true, grep("1", "a"), grep("2", "b"), grep("3", "c"), grep("4", "d"), fake.Turn{Content: "never"})
	s.Send("look around")
	var used []int64
	answers := []string{"1.5", "y"}
	followBudget(t, s, func(p event.Checkpoint) {
		used = append(used, p.Checkpoint.Used)
		if err := s.Answer(p.ID, answers[len(used)-1]); err != nil {
			t.Fatal(err)
		}
	})
	requests, checkpoints, outcome, _ := spendOf(s)
	if requests != 4 || checkpoints != 2 || outcome != "spend_limit" || used[0] != budget.Micros(1) || used[1] != budget.Micros(1.5) {
		t.Fatalf("requests %d, checkpoints %d %v, outcome %s", requests, checkpoints, used, outcome)
	}
	s.Send("go on")
	followBudget(t, s, func(p event.Checkpoint) { t.Fatalf("asked again: %+v", p) })
	if requests, _, outcome, _ := spendOf(s); requests != 4 || outcome != "spend_limit" {
		t.Fatalf("the next turn: requests %d, outcome %s", requests, outcome)
	}
}

// A soft limit raised at a checkpoint is the task's, and holds for the
// turns after it.
func TestSpendSoftLimitRaisedHoldsForLaterTurns(t *testing.T) {
	s, _, _ := spendRig(t, 1, 10, true, grep("1", "a"), grep("2", "b"), fake.Turn{Content: "done"}, grep("3", "c"), fake.Turn{Content: "done again"})
	asked := 0
	answer := func(p event.Checkpoint) {
		asked++
		s.Answer(p.ID, "5")
	}
	s.Send("look around")
	followBudget(t, s, answer)
	s.Send("and again")
	followBudget(t, s, answer)
	if requests, checkpoints, outcome, _ := spendOf(s); requests != 5 || asked != 1 || checkpoints != 1 || outcome != "completed" {
		t.Fatalf("requests %d, asked %d, outcome %s", requests, asked, outcome)
	}
}

// A limit that cannot see a request's cost says so, once.
func TestSpendLimitSaysWhatItCannotCount(t *testing.T) {
	s, _, _ := spendRig(t, 0, 1, false, fake.Turn{Content: "one"}, fake.Turn{Content: "two"})
	for _, text := range []string{"first", "second"} {
		s.Send(text)
		followBudget(t, s, func(p event.Checkpoint) { t.Fatalf("asked: %+v", p) })
	}
	_, _, outcome, notes := spendOf(s)
	said := 0
	for _, n := range notes {
		if strings.Contains(n, "spend limit is set") && strings.Contains(n, "no known cost") {
			said++
		}
	}
	if said != 1 || outcome != "completed" {
		t.Fatalf("notes %q, outcome %s", notes, outcome)
	}
}

// A resumed task counts what it spent before: its log is the record.
func TestSpendLimitCountsAResumedTasksHistory(t *testing.T) {
	s, o, _ := spendRig(t, 0, 5, true, fake.Turn{Content: "one"}, fake.Turn{Content: "never"})
	s.Send("first")
	followBudget(t, s, func(p event.Checkpoint) {})
	id := s.ID()
	s.Close()
	o.Config.Budget.CostHard = 0.5
	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	resumed.Send("second")
	followBudget(t, resumed, func(p event.Checkpoint) {})
	if requests, _, outcome, _ := spendOf(resumed); requests != 1 || outcome != "spend_limit" {
		t.Fatalf("requests %d, outcome %s", requests, outcome)
	}
}
