package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// Compaction measures the context as the window estimates it
// ((context-tokens)); the fake server reports only 279 + 10 prompt tokens
// per message, far fewer, as a real tokenizer may count differently.

// bigLine fills the files the compaction tests read.
const bigLine = "a line of the file being read\n"

// writeTokens writes a file of about tokens of the window's estimate.
func writeTokens(t *testing.T, repo, name string, tokens int) {
	t.Helper()
	text := strings.Repeat(bigLine, tokens*3/len(bigLine)+1)
	if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readPath(id, path string) fake.ToolCall {
	return fake.ToolCall{ID: id, Name: "fs-read", Args: map[string]any{"path": path}}
}

// compactAfter sets the threshold to the context's size now plus extra, so
// compaction comes after a known amount of reading whatever the size of the
// system prompt.
func (r *rig) compactAfter(extra int) {
	r.t.Helper()
	r.eval(fmt.Sprintf("(set! compact-threshold (+ (context-tokens) %d))", extra))
}

func (r *rig) evalInt(src string) int {
	r.t.Helper()
	n, err := strconv.Atoi(r.eval(src))
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

// requests are the recorded llm.req events, each with the prompt tokens the
// server reported for it.
func (r *rig) requests() (reqs []log.LLMReq, reported []int) {
	r.t.Helper()
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		r.t.Fatal(err)
	}
	for _, ev := range evs {
		switch ev.Kind {
		case log.KindLLMReq:
			var p log.LLMReq
			if err := ev.Decode(&p); err != nil {
				r.t.Fatal(err)
			}
			reqs = append(reqs, p)
		case log.KindLLMResp:
			var p log.LLMResp
			if err := ev.Decode(&p); err != nil {
				r.t.Fatal(err)
			}
			reported = append(reported, p.Usage.PromptTokens)
		}
	}
	return reqs, reported
}

type sent struct{ Role, Content string }

func requestMessages(t *testing.T, req fake.Request) []sent {
	t.Helper()
	out := make([]sent, len(req.Messages))
	for i, raw := range req.Messages {
		if err := json.Unmarshal(raw, &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (r *rig) narrowings() int {
	r.t.Helper()
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Kind == log.KindContextNarrow {
			n++
		}
	}
	return n
}

func TestCompactionHandsOverInANarrowedContext(t *testing.T) {
	r := strategyRig(t, "", 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("a", "big.txt")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("b", "big.txt")}},
		fake.Turn{Content: "NOTE: main.go only declares package main; nothing changed yet."},
		fake.Turn{Content: "done"},
	)
	writeTokens(t, r.repo, "big.txt", 1000)
	r.compactAfter(1500) // passed by the second read
	if err := r.turn("make main.go print hello"); err != "" {
		t.Fatal(err)
	}
	if len(r.srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4", len(r.srv.Requests))
	}
	if ask := requestMessages(t, r.srv.Requests[2]); !strings.Contains(ask[len(ask)-1].Content, "write a handover note") {
		t.Fatalf("third request did not ask for the note: %+v", ask[len(ask)-1])
	}
	after := requestMessages(t, r.srv.Requests[3])
	system := r.s.loop.WindowContext().System
	if len(after) != 2 || after[0].Role != "system" || after[0].Content != system || after[1].Role != "user" {
		t.Fatalf("compacted request is not the session prompt and one hand-over: %+v", after)
	}
	handover := after[1].Content
	if !strings.HasPrefix(handover, "make main.go print hello") || !strings.HasSuffix(handover, "Handover note:\nNOTE: main.go only declares package main; nothing changed yet.") {
		t.Fatalf("hand-over:\n%s", handover)
	}
	if strings.Contains(string(r.srv.Requests[3].Raw), strings.TrimSuffix(bigLine, "\n")) {
		t.Fatal("the compacted context still carries the old tool results")
	}
	if n := r.narrowings(); n != 1 {
		t.Fatalf("%d narrowings, want 1", n)
	}
}

// Compaction is on by default, late: just under the window's high water
// (ADR 32), so a short turn never compacts.
func TestCompactionDefaultsUnderTheHighWater(t *testing.T) {
	r := strategyRig(t, "", 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("a")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("b")}},
		fake.Turn{Content: "done"},
	)
	if err := r.turn("make main.go print hello"); err != "" {
		t.Fatal(err)
	}
	if len(r.srv.Requests) != 3 || r.narrowings() != 0 {
		t.Fatalf("%d requests and %d narrowings in a short turn", len(r.srv.Requests), r.narrowings())
	}
	if got := r.eval("compact-threshold"); got != "58983" {
		t.Fatalf("compact-threshold defaults to %s, want 61440 less a twenty-fifth", got)
	}
}

// A model that answers the note request with tool calls has them skipped;
// after compact-attempts replies compaction gives up, the turn goes on in
// the full context, and it is not retried until the context grows.
func TestCompactionGivesUpWithoutANote(t *testing.T) {
	r := strategyRig(t, "", 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("a", "big.txt")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("b", "big.txt")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("c")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("d")}},
		fake.Turn{Content: "done"},
	)
	writeTokens(t, r.repo, "big.txt", 1000)
	r.compactAfter(1500)
	if err := r.turn("make main.go print hello"); err != "" {
		t.Fatal(err)
	}
	if r.narrowings() != 0 {
		t.Fatal("compacted without a note")
	}
	results := r.results()
	for _, id := range []string{"c", "d"} {
		if !strings.Contains(results[id], "write the handover note first") {
			t.Fatalf("call %s during the note request: %q", id, results[id])
		}
	}
	last := requestMessages(t, r.srv.Requests[len(r.srv.Requests)-1])
	if !strings.Contains(last[len(last)-1].Content, "Never mind the handover note") {
		t.Fatalf("the model was not told to go on: %+v", last[len(last)-1])
	}
	if got := r.eval("compact-gave-up-at"); got == "0" {
		t.Fatal("giving up did not record the size")
	}
}

// windowRig is a session whose window trims past high, fixed as config.toml
// would set it, and whose control model's context holds numCtx tokens (0:
// the window's size, as an ollama model without num_ctx is asked for). The
// model answers by respond.
func windowRig(t *testing.T, high, numCtx int, respond func(fake.Request) fake.Turn) *rig {
	t.Helper()
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644)
	srv := fake.NewFunc(respond)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Loop.MaxToolCalls, cfg.Budget.ToolCallsSoft = 200, 150
	cfg.Window.HighWater, cfg.Window.LowWater, cfg.Window.Fixed = high, high/2, true
	cfg.Control.NumCtx = numCtx
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: repo, Version: "test", Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: repo, logb: logb}
}

// baseTokens is the window's estimate of a session's context before the
// first message: the system prompt and the tools.
func baseTokens(t *testing.T) int {
	return strategyRig(t, "", 10).evalInt("(context-tokens)")
}

// reader is a model that reads files by name, one per request, then says
// done; asked for the handover note, it writes one.
func reader(files ...string) func(fake.Request) fake.Turn {
	var mu sync.Mutex
	n := 0
	return func(req fake.Request) fake.Turn {
		var last sent
		json.Unmarshal(req.Messages[len(req.Messages)-1], &last)
		if strings.Contains(last.Content, "write a handover note") {
			return fake.Turn{Content: "NOTE: read the files; nothing changed yet."}
		}
		mu.Lock()
		defer mu.Unlock()
		if n >= len(files) || strings.Contains(last.Content, "Handover note:") {
			return fake.Turn{Content: "done"}
		}
		n++
		return fake.Turn{ToolCalls: []fake.ToolCall{readPath(fmt.Sprintf("r%d", n), files[n-1])}}
	}
}

// The window trims past its high water by its own estimate, and compaction
// measures that estimate, so a long turn is compacted before anything is
// trimmed. The server counts far fewer tokens throughout; compared with its
// prompt-tokens, the threshold was never reached before the window elided
// the oldest reads.
func TestCompactionComesBeforeTheWindowTrims(t *testing.T) {
	high := baseTokens(t) + 20000
	// Each read adds about a hundredth of the high water, so one lands
	// between the threshold, a twenty-fifth below the high water, and the
	// high water, with room for the note request.
	files := make([]string, 100)
	for i := range files {
		files[i] = fmt.Sprintf("step%d.txt", i)
	}
	r := windowRig(t, high, 0, reader(files...))
	for _, f := range files {
		writeTokens(t, r.repo, f, high/100)
	}
	if err := r.turn("read the files"); err != "" {
		t.Fatal(err)
	}
	if n := r.narrowings(); n != 1 {
		t.Fatalf("%d narrowings, want 1", n)
	}
	threshold := r.evalInt("compact-threshold")
	reqs, reported := r.requests()
	note := -1
	for i, q := range reqs {
		if len(q.Elided) > 0 {
			t.Fatalf("request %d trimmed %v before compaction", i, q.Elided)
		}
		if reported[i] >= threshold {
			t.Fatalf("the server reported %d prompt tokens; the test needs it under the threshold %d", reported[i], threshold)
		}
		if note < 0 && q.EstTokens > threshold {
			note = i
		}
	}
	ask := requestMessages(t, r.srv.Requests[note])
	if note < 0 || !strings.Contains(ask[len(ask)-1].Content, "write a handover note") {
		t.Fatalf("the first request past the threshold (%d) is not the note request", note)
	}
	if reqs[note].EstTokens > high {
		t.Fatalf("the note request (%d) is past the high water (%d)", reqs[note].EstTokens, high)
	}
}

// A round that jumps past the high water is still compacted rather than
// trimmed when the model's context has room above the window: the note
// request may use it, up to the ceiling. Without that room it is trimmed.
func TestCompactionUsesTheRoomAboveTheHighWater(t *testing.T) {
	high := baseTokens(t) + 3000
	for _, tc := range []struct {
		name    string
		numCtx  int
		ceiling int
	}{
		{"room", high + 5000 + config.Default().Window.OutputReserve, high + 5000},
		{"no room", 0, high},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := windowRig(t, high, tc.numCtx, reader("step.txt", "big.txt"))
			writeTokens(t, r.repo, "step.txt", 100)
			writeTokens(t, r.repo, "big.txt", 5000) // shown as max_result_tokens (3072)
			if got := r.evalInt("(assoc-ref (window-limits) 'ceiling)"); got != tc.ceiling {
				t.Fatalf("ceiling %d, want %d", got, tc.ceiling)
			}
			if err := r.turn("read the files"); err != "" {
				t.Fatal(err)
			}
			if n := r.narrowings(); n != 1 {
				t.Fatalf("%d narrowings, want 1", n)
			}
			reqs, _ := r.requests()
			if len(reqs) != 4 {
				t.Fatalf("%d requests, want two reads, the note and done", len(reqs))
			}
			note := reqs[2]
			if note.EstTokens <= high && len(note.Elided) == 0 {
				t.Fatalf("the reads did not jump past the high water (%d): the note request is %d", high, note.EstTokens)
			}
			if tc.numCtx > 0 && (len(note.Elided) > 0 || note.HighWater != tc.ceiling) {
				t.Fatalf("the note request was trimmed (%v) or not recorded with the ceiling (%d)", note.Elided, note.HighWater)
			}
			if tc.numCtx == 0 && (len(note.Elided) == 0 || note.HighWater != 0) {
				t.Fatalf("with no room the note request should trim at the high water: elided %v, high water %d", note.Elided, note.HighWater)
			}
			if n := replayAll(t, r.logb); n != 4 {
				t.Fatalf("replayed %d requests", n)
			}
		})
	}
}
