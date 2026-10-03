package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The plan gate is Scheme (internal/loop/plan.scm); these tests drive it
// through a session against a fake judge.

// fakeJudge answers Jev requests in order: a map is the answer to the
// question, an int is an HTTP status to fail with.
type fakeJudge struct {
	*httptest.Server
	mu        sync.Mutex
	answers   []any
	questions []string // the type of each question asked
	states    []string // the state each question was about
	bodies    []string // each request as sent
}

func newFakeJudge(t *testing.T, answers ...any) *fakeJudge {
	j := &fakeJudge{answers: answers}
	j.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			State     string                           `json:"state"`
			Questions map[string]struct{ Type string } `json:"questions"`
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		j.mu.Lock()
		defer j.mu.Unlock()
		j.questions = append(j.questions, body.Questions["q"].Type)
		j.states = append(j.states, body.State)
		j.bodies = append(j.bodies, string(raw))
		if len(j.answers) == 0 {
			http.Error(w, "no scripted answer", http.StatusInternalServerError)
			return
		}
		answer := j.answers[0]
		j.answers = j.answers[1:]
		if status, ok := answer.(int); ok {
			http.Error(w, "judge down", status)
			return
		}
		// The API's documented shape: the model that answered and the usage.
		json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": map[string]any{"q": answer},
			"usage": map[string]int{"input_tokens": len(raw) / 4, "output_tokens": 20}})
	}))
	t.Cleanup(j.Close)
	return j
}

func (j *fakeJudge) asked() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.questions...)
}

// judgedRig is a session with a judge: a jev-key in the keychain, bound to
// the fake judge's host.
func judgedRig(t *testing.T, judge *fakeJudge, init string, turns ...fake.Turn) *rig {
	t.Helper()
	return judgedRigAt(t, judge, config.JevContentPlan, init, turns...)
}

// judgedRigAt is judgedRig at a jev.content level.
func judgedRigAt(t *testing.T, judge *fakeJudge, content, init string, turns ...fake.Turn) *rig {
	t.Helper()
	return judgedRigWith(t, judge, content, init, nil, turns...)
}

// judgedRigWith is judgedRigAt with the session's options adjusted first.
func judgedRigWith(t *testing.T, judge *fakeJudge, content, init string, adjust func(*Options), turns ...fake.Turn) *rig {
	t.Helper()
	repo := t.TempDir()
	// These tests pin the gate's own turns; exploring before a draft has its
	// own test (TestPlannerExploresFirst).
	init = "(define plan-explore-rounds 0)\n" + init
	os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(init), 0o644)
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Jev.Content = content
	var kc *keychain.Store
	if judge != nil {
		cfg.Jev.URL = judge.URL
		var err error
		if kc, err = keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p")); err != nil {
			t.Fatal(err)
		}
		kc.Add("jev-key", []byte("sk-judge-canary-0001"))
	}
	logb := &bytes.Buffer{}
	o := Options{Config: cfg, Repo: repo, Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: logb, Keychain: kc,
		Bindings: binding.Table{Bind: []binding.Binding{{Class: "jev-key", Dest: "127.0.0.1", Positions: []string{"header:Authorization:Bearer"}, Methods: []string{"POST"}}}}}
	if adjust != nil {
		adjust(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: repo, logb: logb}
}

// gatePlans returns the gate.plan records in the log.
func (r *rig) gatePlans() []log.GatePlan {
	r.t.Helper()
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		r.t.Fatal(err)
	}
	var out []log.GatePlan
	for _, ev := range evs {
		if ev.Kind == log.KindGatePlan {
			var gp log.GatePlan
			ev.Decode(&gp)
			out = append(out, gp)
		}
	}
	return out
}

// score is a judge's answer on the five-level rubric: raw level n of 0..4.
func score(n float64) map[string]any { return map[string]any{"score": n, "confidence": 0.9} }
func noul(p float64) map[string]any  { return map[string]any{"noul": p} }

func TestPlanGateWithoutAJudgeDoesNothing(t *testing.T) {
	r := judgedRig(t, nil, "", fake.Turn{Content: "done"})
	if e := r.turn("do a thing"); e != "" {
		t.Fatal(e)
	}
	if plans := r.gatePlans(); len(plans) != 0 || len(r.srv.Requests) != 1 {
		t.Fatalf("plans %+v, model requests %d", plans, len(r.srv.Requests))
	}
}

func TestPlanGateProceedsWhenNoPlanIsNeeded(t *testing.T) {
	judge := newFakeJudge(t, noul(0.2))
	r := judgedRig(t, judge, "", fake.Turn{Content: "done"})
	if e := r.turn("what is this?"); e != "" {
		t.Fatal(e)
	}
	plans := r.gatePlans()
	if len(plans) != 1 || plans[0].Noul != "proceed" || plans[0].Score != 0.2 || plans[0].Plan != "" {
		t.Fatalf("plans %+v", plans)
	}
	if len(r.srv.Requests) != 1 || strings.Join(judge.asked(), " ") != "noul" {
		t.Fatalf("model requests %d, judge asked %v", len(r.srv.Requests), judge.asked())
	}
}

func TestPlanGateAcceptsAGoodPlan(t *testing.T) {
	judge := newFakeJudge(t, noul(0.9), score(3.6))
	r := judgedRig(t, judge, "", fake.Turn{Content: "1. the plan"}, fake.Turn{Content: "done"})
	if e := r.turn("build it"); e != "" {
		t.Fatal(e)
	}
	plans := r.gatePlans()
	if len(plans) != 1 || plans[0].Noul != "plan" || plans[0].Plan != "1. the plan" || plans[0].Score != 0.9 || plans[0].Revisions != 0 {
		t.Fatalf("plans %+v", plans)
	}
	// The plan is in front of the model; the planning conversation is not.
	main := string(r.srv.Requests[1].Raw)
	if !strings.Contains(main, "Plan for the current task") || !strings.Contains(main, "1. the plan") ||
		strings.Contains(main, "Write a short, concrete plan") {
		t.Fatalf("main request:\n%s", main)
	}
	if planner := string(r.srv.Requests[0].Raw); !strings.Contains(planner, "You are nodwyn's planner") {
		t.Fatalf("planner request:\n%s", planner)
	}
}

func TestPlanGateRevisesUntilThePlanScores(t *testing.T) {
	judge := newFakeJudge(t, noul(0.9), score(0.8), score(2), score(3.4))
	r := judgedRig(t, judge, "", fake.Turn{Content: "v1"}, fake.Turn{Content: "v2"}, fake.Turn{Content: "v3"}, fake.Turn{Content: "done"})
	if e := r.turn("hard task"); e != "" {
		t.Fatal(e)
	}
	plans := r.gatePlans()
	if len(plans) != 1 || plans[0].Plan != "v3" || plans[0].Score != 0.85 || plans[0].Revisions != 2 {
		t.Fatalf("plans %+v", plans)
	}
	if revise := string(r.srv.Requests[1].Raw); !strings.Contains(revise, "scored 0.20 out of 1") || !strings.Contains(revise, `Previous plan:\nv1`) {
		t.Fatalf("revision request:\n%s", revise)
	}
}

func TestPlanGateStopsAtTheRevisionBound(t *testing.T) {
	judge := newFakeJudge(t, noul(0.9), score(0.4), score(0.4), score(0.4), score(0.4))
	r := judgedRig(t, judge, "", fake.Turn{Content: "v0"}, fake.Turn{Content: "v1"}, fake.Turn{Content: "v2"}, fake.Turn{Content: "v3"}, fake.Turn{Content: "done"})
	if e := r.turn("impossible"); e != "" {
		t.Fatal(e)
	}
	plans := r.gatePlans()
	if len(plans) != 1 || plans[0].Revisions != 3 || plans[0].Plan != "v0" {
		t.Fatalf("plans %+v", plans)
	}
	if n := len(judge.asked()); n != 5 {
		t.Fatalf("the judge was asked %d times", n)
	}
}

func TestPlanGateDegradesWhenTheJudgeFails(t *testing.T) {
	judge := newFakeJudge(t, http.StatusServiceUnavailable)
	r := judgedRig(t, judge, "", fake.Turn{Content: "1. degraded plan"}, fake.Turn{Content: "done"})
	if e := r.turn("do it"); e != "" {
		t.Fatal(e)
	}
	plans := r.gatePlans()
	if len(plans) != 1 || plans[0].Noul != "degraded" || plans[0].Plan != "1. degraded plan" || !strings.Contains(plans[0].Reason, "jev-noul failed") {
		t.Fatalf("plans %+v", plans)
	}
}

// A run's turn plans whatever the judge would say, and does not ask it.
func TestRequiredPlanSkipsTheQuestion(t *testing.T) {
	judge := newFakeJudge(t, score(3.2))
	// The finish check reminds a planned turn that changed nothing twice.
	done := fake.Turn{Content: "done"}
	r := judgedRig(t, judge, "", fake.Turn{Content: "1. required plan"}, done, done, done)
	if e := r.planned("work"); e != "" {
		t.Fatal(e)
	}
	plans := r.gatePlans()
	if len(plans) != 1 || plans[0].Noul != "plan" || plans[0].Plan != "1. required plan" {
		t.Fatalf("plans %+v", plans)
	}
	if got := strings.Join(judge.asked(), " "); got != "score" {
		t.Fatalf("the judge was asked %q", got)
	}
}

// A project that replaces turn, the only way past the gate, still cannot act
// in a turn that must plan until it records a plan.
func TestRequiredPlanBlocksActionUntilRecorded(t *testing.T) {
	r := judgedRig(t, nil, `(define (nodwyn-turn request) (fs-write "x.txt" "y"))`)
	if err := r.s.SendPlanned("work"); err != nil {
		t.Fatal(err)
	}
	var errText string
	for _, ev := range r.drain("n") {
		if ev.Kind == "error" {
			errText = fmt.Sprint(ev.Payload)
		}
	}
	if !strings.Contains(errText, "must record a plan") {
		t.Fatalf("turn error %q", errText)
	}
	if _, err := os.Stat(filepath.Join(r.repo, "x.txt")); err == nil {
		t.Fatal("an unplanned write ran")
	}
}

// A model sometimes answers the planner with thinking only; one empty sample
// must not end a run that requires a plan, so it is asked again, noted.
func TestPlanGateAsksAgainForAnEmptyPlan(t *testing.T) {
	done := fake.Turn{Content: "done"}
	r := judgedRig(t, nil, "", fake.Turn{Content: ""}, fake.Turn{Content: "1. read\n2. fix"}, done, done, done)
	if e := r.planned("work"); e != "" {
		t.Fatal(e)
	}
	plans := r.gatePlans()
	if len(plans) != 1 || plans[0].Plan != "1. read\n2. fix" {
		t.Fatalf("plans %+v", plans)
	}
	if n := strings.Count(r.logb.String(), "planner returned an empty plan, asking again (1/2)"); n != 1 {
		t.Fatalf("retry notes: %d", n)
	}
}

// A revision that comes back empty keeps the best plan instead of replacing
// it with nothing.
func TestPlanGateKeepsTheBestPlanWhenARevisionIsEmpty(t *testing.T) {
	judge := newFakeJudge(t, noul(0.9), score(0.8))
	r := judgedRig(t, judge, "", fake.Turn{Content: "first plan"}, fake.Turn{Content: ""}, fake.Turn{Content: ""}, fake.Turn{Content: ""}, fake.Turn{Content: "done"})
	if e := r.turn("work"); e != "" {
		t.Fatal(e)
	}
	plans := r.gatePlans()
	if len(plans) != 1 || plans[0].Plan != "first plan" || plans[0].Score != 0.2 || plans[0].Revisions != 0 {
		t.Fatalf("plans %+v", plans)
	}
}

// A strategy that redefines agent-turn without the gate still plans in a run:
// turn plans first. The planner is offered no tools, and its request, like
// the strategy's, replays from the log.
func TestCustomStrategyIsPlannedInARun(t *testing.T) {
	r := judgedRig(t, nil, `(define (agent-turn request)
  (let loop ((reply (model-turn)))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (unless (null? calls) (run-tools calls) (loop (model-turn))))))`,
		fake.Turn{Content: "1. write x.txt"},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "x.txt", "content": "y"}}}},
		fake.Turn{Content: "done"})
	r.s.opts.AutoApprove = true
	if err := r.s.SendPlanned("write x.txt"); err != nil {
		t.Fatal(err)
	}
	r.drain("y")
	if plans := r.gatePlans(); len(plans) != 1 || plans[0].Plan != "1. write x.txt" {
		t.Fatalf("plans %+v", plans)
	}
	if strings.Contains(string(r.srv.Requests[0].Raw), `"tools"`) || !strings.Contains(string(r.srv.Requests[1].Raw), `"tools"`) {
		t.Fatal("the planner was offered tools, or the strategy was not")
	}
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	reqs := 0
	for _, ev := range evs {
		if ev.Kind != log.KindLLMReq {
			continue
		}
		reqs++
		if _, err := window.ReplayRequest(evs, ev.Seq); err != nil {
			t.Fatalf("request %d: %v", ev.Seq, err)
		}
	}
	if reqs != 3 {
		t.Fatalf("%d requests", reqs)
	}
}

// The turn's plan flags end with it: an evaluation after a required turn that
// failed unplanned is not held to that turn's plan.
func TestPlanFlagsEndWithTheTurn(t *testing.T) {
	r := judgedRig(t, nil, `(define (nodwyn-turn request) (fs-write "x.txt" "y"))`)
	if err := r.s.SendPlanned("work"); err != nil {
		t.Fatal(err)
	}
	r.drain("n")
	if got := r.eval(`(list (plan-required?) (plan-recorded?))`); got != "(#f #f)" {
		t.Fatalf("plan flags after the turn: %s", got)
	}
}
