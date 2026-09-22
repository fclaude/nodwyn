package core

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

func TestProgressStopsTurnAndAllowsNextMessage(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		count  int
		tool   string
		args   func(int) map[string]any
		reason string
	}{
		{"repeat", 4, "fs-list", func(int) map[string]any { return map[string]any{"path": "."} }, "four times"},
		{"denials", 4, "fs-write", func(int) map[string]any { return map[string]any{"path": "denied", "content": "no"} }, "denied call"},
		{"errors", 10, "fs-read", func(i int) map[string]any { return map[string]any{"path": fmt.Sprintf("missing-%d", i)} }, "ten consecutive"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var turns []fake.Turn
			for i := 0; i < scenario.count; i++ {
				turns = append(turns, fake.Turn{ToolCalls: []fake.ToolCall{{ID: fmt.Sprint(i), Name: scenario.tool, Args: scenario.args(i)}}})
			}
			turns = append(turns, fake.Turn{Content: "fresh turn"})
			srv := fake.New(turns...)
			defer srv.Close()
			o := diskOptions(t)
			o.Provider = llm.NewOllama(srv.URL)
			s, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.Send("test progress"); err != nil {
				t.Fatal(err)
			}
			r := &rig{t: t, s: s}
			events := r.drain("n")
			foundError, foundNote := false, false
			for _, ev := range events {
				if ev.Kind == event.Error && strings.Contains(ev.Payload.(event.ErrorInfo).Err, scenario.reason) {
					foundError = true
				}
				if ev.Kind == string(log.KindNote) && strings.Contains(ev.Payload.(log.Note).Text, scenario.reason) {
					foundNote = true
				}
			}
			if !foundError || !foundNote {
				t.Fatalf("missing visible/durable stop: %s", kinds(events))
			}
			if len(srv.Requests) != scenario.count {
				t.Fatal("made another model request after stop")
			}
			if err := s.Send("continue with a new approach"); err != nil {
				t.Fatal(err)
			}
			r.drain("n")
			if len(srv.Requests) != scenario.count+1 {
				t.Fatal("next turn did not proceed")
			}
		})
	}
}

func TestProgressStopCompletesBatchWithoutExecutingRemainder(t *testing.T) {
	var batch []fake.ToolCall
	for i := 0; i < 5; i++ {
		batch = append(batch, fake.ToolCall{ID: fmt.Sprint(i), Name: "fs-list", Args: map[string]any{"path": "."}})
	}
	srv := fake.New(fake.Turn{ToolCalls: batch}, fake.Turn{Content: "new approach"})
	defer srv.Close()
	o := diskOptions(t)
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Send("repeat")
	r := &rig{t: t, s: s}
	r.drain("n")
	calls, results := 0, 0
	for _, ev := range s.EventsSoFar() {
		if ev.Kind == log.KindToolCall {
			calls++
		}
		if ev.Kind == log.KindToolResult {
			results++
			var result log.ToolResult
			ev.Decode(&result)
			if result.ID == "4" && (result.OK || !strings.Contains(result.Error, "Not executed")) {
				t.Fatalf("last result: %+v", result)
			}
		}
	}
	if calls != 4 || results != 5 {
		t.Fatalf("calls=%d results=%d", calls, results)
	}
	s.Send("continue differently")
	r.drain("n")
	if !strings.Contains(string(srv.Requests[1].Raw), "Not executed") {
		t.Fatal("skipped result missing from next context")
	}
}
