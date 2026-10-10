package core

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// Every request caps its output at window.output_reserve, so prompt and
// output fit num_ctx; a reply cut at that cap is noted in the log and, with
// no tool call to answer, asked again with a note to be briefer. Both
// requests replay from the log.
func TestOutputIsCappedAndACutReplyIsAskedAgain(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "", 10,
		fake.Turn{Thinking: "long thinking", DoneReason: "length"},
		fake.Turn{Content: "done"},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 2 {
		t.Fatalf("%d requests, want 2", len(r.srv.Requests))
	}
	cap := `"num_predict":` + strconv.Itoa(config.Default().Window.OutputReserve)
	if !strings.Contains(string(r.srv.Requests[0].Raw), cap) {
		t.Fatalf("request does not cap its output with %s:\n%s", cap, r.srv.Requests[0].Raw)
	}
	if !strings.Contains(string(r.srv.Requests[1].Raw), "cut off at the output limit") {
		t.Fatal("the model was not told its reply was cut off")
	}
	if !strings.Contains(r.logb.String(), "the reply stopped at the output limit") {
		t.Fatal("the cut is not in the log")
	}
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Kind == log.KindLLMReq {
			if _, err := window.ReplayRequest(evs, ev.Seq); err != nil {
				t.Fatalf("request %d: %v", ev.Seq, err)
			}
		}
	}
}

// Cuts are asked about at most output-cut-retries times; then the reply
// stands, as any reply without calls does.
func TestCutRepliesAreRetriedAtMostTwice(t *testing.T) {
	t.Parallel()
	cut := fake.Turn{Thinking: "long thinking", DoneReason: "length"}
	r := strategyRig(t, "", 10, cut, cut, cut, fake.Turn{Content: "never asked"})
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 3 {
		t.Fatalf("%d requests, want 3", len(r.srv.Requests))
	}
}

// A reply with no words and no tool call would end the turn with nothing to
// read: the plain ask is made again once, and the model is told why (ADR 32,
// amended 2026-10-06). The one retry is all there is: a second empty reply
// stands.
func TestAnEmptyReplyIsAskedAgainOnce(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "", 10, fake.Turn{Thinking: "hm"}, fake.Turn{Content: "done"})
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 2 {
		t.Fatalf("%d requests, want 2", len(r.srv.Requests))
	}
	if !strings.Contains(string(r.srv.Requests[1].Raw), "Your previous reply was empty") {
		t.Fatal("the model was not told its reply was empty")
	}

	r = strategyRig(t, "", 10, fake.Turn{Thinking: "hm"}, fake.Turn{Thinking: "hm"}, fake.Turn{Content: "never asked"})
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 2 {
		t.Fatalf("%d requests, want 2", len(r.srv.Requests))
	}
}

// A model's own max_output caps its replies instead of the window's, which a
// remote reasoning model may need; the request records it, so it replays.
func TestAModelMayCapItsOwnReplies(t *testing.T) {
	t.Parallel()
	srv := fake.NewT(t, fake.Turn{Thinking: "long thinking", DoneReason: "length"}, fake.Turn{Content: "done"})
	defer srv.Close()
	cfg := config.Default()
	cfg.Control.MaxOutput = 2048
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), Version: "test", Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := &rig{t: t, s: s, srv: srv, logb: logb}
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if !strings.Contains(string(srv.Requests[0].Raw), `"num_predict":2048`) {
		t.Fatalf("request does not cap its output at the model's max_output:\n%s", srv.Requests[0].Raw)
	}
	if !strings.Contains(logb.String(), "the reply stopped at the output limit (2048 tokens, the reply cap of model control)") {
		t.Fatal("the cut's note does not name the model's cap")
	}
	evs, err := log.Read(bytes.NewReader(logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Kind == log.KindLLMReq {
			if _, err := window.ReplayRequest(evs, ev.Seq); err != nil {
				t.Fatalf("request %d: %v", ev.Seq, err)
			}
		}
	}
}

// A model's temperature and seed go with each of its requests, in ollama's
// options, and are recorded so the request replays; unset, nothing is sent.
func TestAModelMaySetSampling(t *testing.T) {
	t.Parallel()
	srv := fake.NewT(t, fake.Turn{Content: "done"})
	defer srv.Close()
	cfg := config.Default()
	temperature, seed := 0.0, int64(42)
	cfg.Control.Temperature, cfg.Control.Seed = &temperature, &seed
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), Version: "test", Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := &rig{t: t, s: s, srv: srv, logb: logb}
	if e := r.turn("hello"); e != "" {
		t.Fatal(e)
	}
	raw := string(srv.Requests[0].Raw)
	if !strings.Contains(raw, `"temperature":0`) || !strings.Contains(raw, `"seed":42`) {
		t.Fatalf("sampling settings not sent:\n%s", raw)
	}
	evs, err := log.Read(bytes.NewReader(logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	replayed := 0
	for _, ev := range evs {
		if ev.Kind == log.KindLLMReq {
			req, err := window.ReplayRequest(evs, ev.Seq)
			if err != nil {
				t.Fatalf("request %d: %v", ev.Seq, err)
			}
			if req.Seed == nil || *req.Seed != 42 || req.Temperature == nil || *req.Temperature != 0 {
				t.Fatalf("replayed request lost its sampling: %+v", req)
			}
			replayed++
		}
	}
	if replayed == 0 {
		t.Fatal("no request replayed")
	}
}

// A reply cut off while it weighed options is told to settle them with a
// run, not by which is shorter or which the plan named: told only to think
// less, a model on xarray-6599 dropped the option its runs had shown right
// for "the minimal fix … (the plan)" (ADR 32, amended 2026-10-10).
func TestACutReplyIsToldToSettleOptionsWithARun(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "", 10,
		fake.Turn{Thinking: "option A or option B", DoneReason: "length"},
		fake.Turn{Content: "done"},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 2 || !strings.Contains(string(r.srv.Requests[1].Raw), "run the check that tells them apart") {
		t.Fatalf("%d requests; the cut reply was not told to settle options with a run", len(r.srv.Requests))
	}
}
