package core

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/window"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

// Every request caps its output at window.output_reserve, so prompt and
// output fit num_ctx; a reply cut at that cap is noted in the log and, with
// no tool call to answer, asked again with a note to be briefer. Both
// requests replay from the log.
func TestOutputIsCappedAndACutReplyIsAskedAgain(t *testing.T) {
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
	cut := fake.Turn{Thinking: "long thinking", DoneReason: "length"}
	r := strategyRig(t, "", 10, cut, cut, cut, fake.Turn{Content: "never asked"})
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 3 {
		t.Fatalf("%d requests, want 3", len(r.srv.Requests))
	}
}
