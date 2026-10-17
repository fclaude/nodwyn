package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
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

func (r *rig) logged() []log.Event {
	r.t.Helper()
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		r.t.Fatal(err)
	}
	return evs
}

func (r *rig) narrowings() int {
	r.t.Helper()
	n := 0
	for _, ev := range r.logged() {
		if ev.Kind == log.KindContextNarrow {
			n++
		}
	}
	return n
}

// notes returns the text of every note in the log.
func (r *rig) notes() []string {
	r.t.Helper()
	var out []string
	for _, ev := range r.logged() {
		if ev.Kind == log.KindNote {
			var p log.Note
			if err := ev.Decode(&p); err != nil {
				r.t.Fatal(err)
			}
			out = append(out, p.Text)
		}
	}
	return out
}

// compactions returns every context.compact in the log.
func (r *rig) compactions() []log.ContextCompact {
	r.t.Helper()
	var out []log.ContextCompact
	for _, ev := range r.logged() {
		if ev.Kind == log.KindContextCompact {
			var p log.ContextCompact
			if err := ev.Decode(&p); err != nil {
				r.t.Fatal(err)
			}
			out = append(out, p)
		}
	}
	return out
}

// replaysAll checks that every recorded request rebuilds from the log, so a
// compacted window replays like any other.
func (r *rig) replaysAll() {
	r.t.Helper()
	if n := replayAll(r.t, r.logb); n != len(r.srv.Requests) {
		r.t.Fatalf("%d requests replayed, %d sent", n, len(r.srv.Requests))
	}
}

func TestCompactionHandsOverInANarrowedContext(t *testing.T) {
	t.Parallel()
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
	// The record says what set it off and how much it saved; the size after
	// is the next request's.
	c := r.compactions()
	var req log.LLMReq
	var reqs []log.LLMReq
	for _, ev := range r.logged() {
		if ev.Kind == log.KindLLMReq && ev.Decode(&req) == nil {
			reqs = append(reqs, req)
		}
	}
	if len(c) != 1 || c[0].Trigger != "auto" || !c[0].Compacted || c[0].Threshold <= 0 || c[0].Before <= c[0].Threshold ||
		c[0].After <= 0 || c[0].After >= c[0].Before || c[0].After != reqs[3].EstTokens || c[0].NoteSeq == 0 || c[0].NoteTokens <= 0 || c[0].Truncated {
		t.Fatalf("context.compact %+v (next request ~%d)", c, reqs[3].EstTokens)
	}
}

// A compaction gives the turn's plan back after the handover note, under
// the plan header and with the steps marked so far: the window keeps only
// what follows the narrowing, and on commit-bench (dsl16, 6e18b7ba) every
// compaction left the system prompt and the note alone, the note put the
// plan in a file that did not hold it, and the model read the code again
// to rebuild it (ADR 32, amended 2026-10-15).
func TestCompactionCarriesThePlanWithItsMarks(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "", 10,
		fake.Turn{ToolCalls: []fake.ToolCall{planWriteCall("w", shapedPlan)}},
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("m", 3, "skipped", "no doc describes main.go"), readPath("a", "big.txt")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("b", "big.txt")}},
		fake.Turn{Content: "NOTE: main.go only declares package main; nothing changed yet."},
		fake.Turn{Content: "done"},
	)
	writeTokens(t, r.repo, "big.txt", 1000)
	r.compactAfter(2000) // passed by the second read, with the plan and its mark
	if err := r.turn("make main.go print hello"); err != "" {
		t.Fatal(err)
	}
	if len(r.srv.Requests) != 5 || r.narrowings() != 1 {
		t.Fatalf("%d requests and %d narrowings, want 5 and 1", len(r.srv.Requests), r.narrowings())
	}
	if ask := lastMessage(r, 3); !strings.Contains(ask, "write a handover note") ||
		!strings.Contains(ask, "do not say a file holds it") {
		t.Fatalf("the note request does not say where the plan is kept:\n%s", ask)
	}
	// The plan recorded again (plan-carry, TestCompactionKeepsTheTurnsPlan)
	// comes before this message: the merge with main kept both (ADR 32,
	// amended 2026-10-16, "the benchmark stack merged with main").
	after := requestMessages(t, r.srv.Requests[4])
	if len(after) != 4 || after[1].Role != "user" || !strings.HasSuffix(after[1].Content, "Handover note:\nNOTE: main.go only declares package main; nothing changed yet.") ||
		!strings.Contains(after[2].Content, shapedPlan) {
		t.Fatalf("the compacted request is not the system prompt, the hand-over, the plan recorded again and the plan: %+v", after)
	}
	header := strings.Trim(r.eval("plan-header"), `"`)
	want := header + "\n" + shapedPlan + "\n\nMarked before the conversation was cleared:\nStep 3 skipped: no doc describes main.go"
	if plan := after[3]; plan.Role != "user" || !strings.HasPrefix(plan.Content, want) || !strings.Contains(plan.Content, "it is in no file in the repository") {
		t.Fatalf("the plan after the hand-over:\n%s\nwant it to start:\n%s", plan.Content, want)
	}
	if c := r.compactions(); len(c) != 1 || c[0].After != requestEst(t, r, 4) {
		t.Fatalf("context.compact %+v: the size after must count the plan", c)
	}
	r.replaysAll()
}

// requestEst is the window's estimate of request i, as its llm.req records it.
func requestEst(t *testing.T, r *rig, i int) int {
	t.Helper()
	reqs, _ := r.requests()
	return reqs[i].EstTokens
}

// Compaction is on by default, late: just under the window's high water
// (ADR 32), so a short turn never compacts.
func TestCompactionDefaultsUnderTheHighWater(t *testing.T) {
	t.Parallel()
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
	if got := r.eval("(compact-limit)"); got != "58983" {
		t.Fatalf("compact-threshold defaults to %s, want 61440 less a twenty-fifth", got)
	}
}

// A model that answers the note request with tool calls has them skipped;
// after compact-attempts replies compaction gives up, the turn goes on in
// the full context, and it is not retried until the context grows.
func TestCompactionGivesUpWithoutANote(t *testing.T) {
	t.Parallel()
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
	if c := r.compactions(); len(c) != 1 || c[0].Compacted || c[0].Trigger != "auto" || c[0].Before <= c[0].Threshold || c[0].After != 0 {
		t.Fatalf("a compaction that gave up: %+v", c)
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
	t.Parallel()
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
	threshold := r.evalInt("(compact-limit)")
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
// request may use it, up to the ceiling. Without that room the normal trim
// rules apply, but these two recent call/result pairs are still protected.
func TestCompactionUsesTheRoomAboveTheHighWater(t *testing.T) {
	t.Parallel()
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
			if tc.numCtx == 0 && (len(note.Elided) != 0 || note.HighWater != 0) {
				t.Fatalf("without headroom the recent results must still keep their calls: elided %v, high water %d", note.Elided, note.HighWater)
			}
			if n := replayAll(t, r.logb); n != 4 {
				t.Fatalf("replayed %d requests", n)
			}
		})
	}
}

// A compaction lasts: the next turn goes on from the handover note, under
// the session's system prompt, with its own request, not from the whole
// conversation again.
func TestCompactionLastsIntoTheNextTurn(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "", 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("a", "big.txt")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("b", "big.txt")}},
		fake.Turn{Content: "NOTE: main.go only declares package main; nothing changed yet."},
		fake.Turn{Content: "done"},
		fake.Turn{Content: "It prints nothing yet."},
	)
	writeTokens(t, r.repo, "big.txt", 1000)
	r.compactAfter(1500) // passed by the second read
	if err := r.turn("make main.go print hello"); err != "" {
		t.Fatal(err)
	}
	if err := r.turn("what does it print?"); err != "" {
		t.Fatal(err)
	}
	next := requestMessages(t, r.srv.Requests[4])
	system := r.s.loop.WindowContext().System
	if len(next) != 4 || next[0].Content != system ||
		!strings.HasPrefix(next[1].Content, "make main.go print hello") || next[2].Content != "done" || next[3].Content != window.DefaultTaskHeader+"\nwhat does it print?" {
		t.Fatalf("the turn after a compaction: %+v", next)
	}
	if strings.Contains(string(r.srv.Requests[4].Raw), strings.TrimSuffix(bigLine, "\n")) {
		t.Fatal("the next turn brought back the old tool results")
	}
	r.replaysAll()
}

// The user compacts whenever they like with /compact: whatever the size, the
// model writes a handover note for the latest request before it, the turn
// ends there, and the next message goes on from the note.
func TestCompactOnRequest(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "", 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("a")}},
		fake.Turn{Content: "main.go declares package main."},
		fake.Turn{Content: "NOTE: main.go only declares package main."},
		fake.Turn{Content: "It prints nothing."},
	)
	if err := r.turn("what does main.go do?"); err != "" {
		t.Fatal(err)
	}
	if err := r.turn("/compact the package name"); err != "" {
		t.Fatal(err)
	}
	if len(r.srv.Requests) != 3 {
		t.Fatalf("%d requests after /compact, want 3: only the note is asked for", len(r.srv.Requests))
	}
	ask := requestMessages(t, r.srv.Requests[2])
	if last := ask[len(ask)-1].Content; !strings.Contains(last, "write a handover note") ||
		!strings.HasSuffix(last, "keep in particular: the package name") {
		t.Fatalf("the note request: %s", last)
	}
	var narrow log.ContextNarrow
	for _, ev := range r.logged() {
		if ev.Kind == log.KindContextNarrow {
			if err := ev.Decode(&narrow); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !narrow.Lasting || narrow.Agent != "compaction" || r.narrowings() != 1 {
		t.Fatalf("compaction's narrowing: %+v", narrow)
	}
	if notes := r.notes(); len(notes) == 0 || !strings.HasPrefix(notes[len(notes)-1], "Compacted") {
		t.Fatalf("the user is not told: %q", notes)
	}
	if c := r.compactions(); len(c) != 1 || c[0].Trigger != "command" || c[0].Focus != "the package name" || !c[0].Compacted || c[0].Threshold != 0 {
		t.Fatalf("/compact's record: %+v", c)
	}

	if err := r.turn("does it print anything?"); err != "" {
		t.Fatal(err)
	}
	after := requestMessages(t, r.srv.Requests[3])
	system := r.s.loop.WindowContext().System
	if len(after) != 3 || after[0].Content != system || after[2].Content != window.DefaultTaskHeader+"\ndoes it print anything?" {
		t.Fatalf("the turn after /compact: %+v", after)
	}
	if handover := after[1].Content; !strings.HasPrefix(handover, "what does main.go do?") ||
		!strings.HasSuffix(handover, "Handover note:\nNOTE: main.go only declares package main.") {
		t.Fatalf("hand-over:\n%s", handover)
	}
	if raw := string(r.srv.Requests[3].Raw); strings.Contains(raw, `package main\n`) || strings.Contains(raw, "/compact") {
		t.Fatal("the compacted conversation still carries what came before the note")
	}
	r.replaysAll()
}

// /compact with nothing before it asks the model nothing; one after another
// hands over the latest request that is not a /compact, and the new note is
// written from the old.
func TestCompactOnRequestHandsOverTheLatestRequest(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "", 10,
		fake.Turn{Content: "hello"},
		fake.Turn{Content: "NOTE 1"},
		fake.Turn{Content: "NOTE 2"},
		fake.Turn{Content: "bye"},
	)
	if err := r.turn("/compact"); err != "" {
		t.Fatal(err)
	}
	if len(r.srv.Requests) != 0 || r.narrowings() != 0 {
		t.Fatal("compacted an empty conversation")
	}
	if notes := r.notes(); len(notes) != 1 || !strings.HasPrefix(notes[0], "Nothing to compact") {
		t.Fatalf("notes: %q", notes)
	}
	for _, text := range []string{"say hello", "/compact", "/compact"} {
		if err := r.turn(text); err != "" {
			t.Fatal(err)
		}
	}
	if ask := requestMessages(t, r.srv.Requests[2]); !strings.HasSuffix(ask[1].Content, "Handover note:\nNOTE 1") {
		t.Fatalf("the second note was not written from the first: %+v", ask)
	}
	if err := r.turn("say bye"); err != "" {
		t.Fatal(err)
	}
	last := requestMessages(t, r.srv.Requests[3])
	if len(last) != 3 || !strings.HasPrefix(last[1].Content, "say hello") || !strings.HasSuffix(last[1].Content, "Handover note:\nNOTE 2") {
		t.Fatalf("after two compactions: %+v", last)
	}
	r.replaysAll()
}

// A strategy asks for compaction at the next round with compact-next-round,
// whatever the context's size; the request is used once.
func TestCompactNextRound(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, `
(define base-run-calls run-calls)
(define (run-calls calls)
  (when (equal? (assoc-ref (car calls) 'id) "a") (compact-next-round "what the read showed"))
  (base-run-calls calls))`, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("a")}},
		fake.Turn{Content: "NOTE: main.go declares package main."},
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("b")}},
		fake.Turn{Content: "done"},
	)
	if err := r.turn("make main.go print hello"); err != "" {
		t.Fatal(err)
	}
	if len(r.srv.Requests) != 4 || r.narrowings() != 1 {
		t.Fatalf("%d requests and %d narrowings, want 4 and 1", len(r.srv.Requests), r.narrowings())
	}
	if ask := requestMessages(t, r.srv.Requests[1]); !strings.HasSuffix(ask[len(ask)-1].Content, "keep in particular: what the read showed") {
		t.Fatalf("the note request: %+v", ask[len(ask)-1])
	}
	if after := requestMessages(t, r.srv.Requests[2]); len(after) != 2 || !strings.HasPrefix(after[1].Content, "make main.go print hello") {
		t.Fatalf("the round after: %+v", after)
	}
	if got := r.eval("compact-requested"); got != "#f" {
		t.Fatalf("the request was not used up: %s", got)
	}
}

// narrow-context's options are strict: a slip is an error, not a silent no.
func TestNarrowContextOptions(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, `
(define refusals '())
(define (agent-turn request)
  (for-each (lambda (options)
              (guard (e (else (set! refusals (cons (assoc-ref e 'message "") refusals))))
                (narrow-context "x" "y" options)))
            '(((lasting #t)) ((kept . #t)) 5)))`, 10)
	if err := r.turn("go"); err != "" {
		t.Fatal(err)
	}
	got := r.eval("refusals")
	for _, want := range []string{"the lasting option is #t or #f", "unknown narrow-context option", "options must be an alist"} {
		if !strings.Contains(got, want) {
			t.Errorf("no refusal %q in %s", want, got)
		}
	}
	if r.narrowings() != 0 {
		t.Fatal("a refused narrowing was recorded")
	}
}

// circlingTurns are a turn that reads two files, writes its handover note,
// and reads two more without writing anything: its context fills a second
// time with nothing written since it compacted. then is what the model does
// after that.
func circlingTurns(then ...fake.Turn) []fake.Turn {
	return append([]fake.Turn{
		{ToolCalls: []fake.ToolCall{readPath("a", "a.txt")}},
		{ToolCalls: []fake.ToolCall{readPath("b", "b.txt")}},
		{Content: "NOTE: the fix goes in fix.txt."},
		{ToolCalls: []fake.ToolCall{readPath("c", "c.txt")}},
		{ToolCalls: []fake.ToolCall{readPath("d", "d.txt")}},
	}, then...)
}

// circlingRig runs circlingTurns(then...) as one turn, the context compacted
// past 1,500 tokens of reading.
func circlingRig(t *testing.T, init string, then ...fake.Turn) *rig {
	t.Helper()
	r := strategyRig(t, init, 30, circlingTurns(then...)...)
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		writeTokens(t, r.repo, name, 1000)
	}
	r.compactAfter(1500)
	if err := r.turn("fix the bug"); err != "" {
		t.Fatal(err)
	}
	if c := r.compactions(); len(c) != 1 {
		t.Fatalf("%d compactions, want 1: %+v", len(c), c)
	}
	stopped := false
	for _, n := range r.notes() {
		stopped = stopped || strings.Contains(n, "no file written since it was last compacted")
	}
	if !stopped {
		t.Fatalf("no note says why the turn stopped: %q", r.notes())
	}
	return r
}

// offeredTools are the names of the tools request i offered, sorted.
func offeredTools(t *testing.T, r *rig, i int) []string {
	t.Helper()
	var out []string
	for _, raw := range r.srv.Requests[i].Tools {
		var tool struct {
			Function struct{ Name string } `json:"function"`
		}
		if err := json.Unmarshal(raw, &tool); err != nil {
			t.Fatal(err)
		}
		out = append(out, tool.Function.Name)
	}
	slices.Sort(out)
	return out
}

// A turn whose context fills a second time with nothing written since the
// first compaction is not compacted again: in session
// 20261004T182412Z-747045aa five compactions in seven minutes made 103
// reads, 88 of files read before, and no edit (compact.scm,
// compact-stop-unchanged). It gets a short write round first, offered the
// edit tools alone, to make the edits its note and plan name: on
// commit-bench (dsl16, 6e18b7ba) the stop asked at once, without tools, for
// a report, which said all six edits were worked out, and the patch was
// empty (ADR 32, amended 2026-10-15). A call to another tool is not run,
// and the report ends the turn.
func TestCompactionGoingRoundInCirclesGetsAWriteRound(t *testing.T) {
	t.Parallel()
	r := circlingRig(t, "",
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "fix.txt", "content": "fixed\n"}}, readPath("e", "a.txt")}},
		fake.Turn{Content: "REPORT: fix.txt written; its test is left."},
	)
	if len(r.srv.Requests) != 7 {
		t.Fatalf("%d requests, want 7", len(r.srv.Requests))
	}
	ask := lastMessage(r, 5)
	if !strings.Contains(ask, "make the code changes now; tests and docs come after") || !strings.Contains(ask, "at most 6 rounds") ||
		!strings.Contains(ask, "Then report what is done and what is left") {
		t.Fatalf("the write round's request: %q", ask)
	}
	want := []string{"fs-edit", "fs-write", "read-definition"} // go-build is a repository's tool
	for _, i := range []int{5, 6} {
		if got := offeredTools(t, r, i); !slices.Equal(got, want) {
			t.Fatalf("request %d offered %v, want %v", i, got, want)
		}
	}
	if data, err := os.ReadFile(filepath.Join(r.repo, "fix.txt")); err != nil || string(data) != "fixed\n" {
		t.Fatalf("the write round's edit was not made: %q, %v", data, err)
	}
	results := r.results()
	if results["w"] != "" || !strings.HasPrefix(results["e"], "Not executed: the turn is ending, and only fs-edit, fs-write, read-definition may be used now") {
		t.Fatalf("the write round ran %q and %q", results["w"], results["e"])
	}
}

// A write round lasts compact-write-rounds requests at most, and is not
// compacted although its writes leave the context past the threshold: a
// third compaction would send the model round again. Then the report is
// asked without tools, and a call the model makes anyway is not run.
func TestAWriteRoundEndsWithAReportWithoutTools(t *testing.T) {
	t.Parallel()
	var writes []fake.Turn
	for i := 1; i <= 6; i++ {
		writes = append(writes, fake.Turn{ToolCalls: []fake.ToolCall{{ID: fmt.Sprint("w", i), Name: "fs-write", Args: map[string]any{"path": "fix.txt", "content": fmt.Sprint("fix ", i, "\n")}}}})
	}
	r := circlingRig(t, "", append(writes, fake.Turn{Content: "REPORT: fix.txt written six times.", ToolCalls: []fake.ToolCall{readPath("x", "a.txt")}})...)
	if len(r.srv.Requests) != 12 {
		t.Fatalf("%d requests, want 12: two reads, the note, two reads, six rounds and the report", len(r.srv.Requests))
	}
	if got := offeredTools(t, r, 10); len(got) == 0 {
		t.Fatal("the sixth round was offered no tools")
	}
	last := r.srv.Requests[11]
	if strings.Contains(string(last.Raw), `"tools":[`) {
		t.Fatal("the report was asked with tools")
	}
	if ask := lastMessage(r, 11); !strings.Contains(ask, "tools are off now") {
		t.Fatalf("the last request did not ask for the report: %q", ask)
	}
	if got := r.results()["x"]; !strings.HasPrefix(got, "Not executed: tools are off now") {
		t.Fatalf("a call with the report: %q", got)
	}
	if data, _ := os.ReadFile(filepath.Join(r.repo, "fix.txt")); string(data) != "fix 6\n" {
		t.Fatalf("fix.txt is %q after six rounds", data)
	}
}

// With compact-write-rounds at 0 the turn stops with the report alone,
// asked without tools, as before the write round.
// A write between the two compactions is progress: the turn compacts again.
func TestCompactionGoingRoundInCirclesStopsTheTurn(t *testing.T) {
	t.Parallel()
	r := circlingRig(t, "(define compact-write-rounds 0)",
		fake.Turn{Content: "REPORT: read four files; the bug is in none of them."},
	)
	if len(r.srv.Requests) != 6 {
		t.Fatalf("%d requests, want 6", len(r.srv.Requests))
	}
	last := r.srv.Requests[5]
	if strings.Contains(string(last.Raw), `"tools":[`) {
		t.Fatal("the report was asked with tools")
	}
	if ask := requestMessages(t, last); !strings.Contains(ask[len(ask)-1].Content, "Stop here") {
		t.Fatalf("the last request did not ask for the report: %+v", ask[len(ask)-1])
	}
	w := strategyRig(t, "", 20,
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("a", "a.txt")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("b", "b.txt")}},
		fake.Turn{Content: "NOTE: the fix goes in fix.txt."},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "fix.txt", "content": "fixed\n"}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("c", "c.txt")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("d", "d.txt")}},
		fake.Turn{Content: "NOTE: fix.txt written; checking the rest."},
		fake.Turn{Content: "done"},
	)
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		writeTokens(t, w.repo, name, 1000)
	}
	w.compactAfter(1500)
	if err := w.turn("fix the bug"); err != "" {
		t.Fatal(err)
	}
	if c := w.compactions(); len(c) != 2 {
		t.Fatalf("%d compactions after a write, want 2", len(c))
	}
}

// A handover note cut off at the output limit is asked for again, shorter:
// on django-15280 two notes spent the reply on thinking and kept one
// sentence, all the run had to go on after the clear (ADR 32, amended
// 2026-10-10).
func TestACutHandoverNoteIsAskedForAgain(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "", 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("a", "big.txt")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("b", "big.txt")}},
		fake.Turn{Content: "I have gathered substantial evidence. Let me record", DoneReason: "length"},
		fake.Turn{Content: "NOTE: main.go only declares package main."},
		fake.Turn{Content: "done"},
	)
	writeTokens(t, r.repo, "big.txt", 1000)
	r.compactAfter(1500) // passed by the second read
	if err := r.turn("make main.go print hello"); err != "" {
		t.Fatal(err)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5", len(r.srv.Requests))
	}
	if ask := requestMessages(t, r.srv.Requests[3]); !strings.Contains(ask[len(ask)-1].Content, "handover note was cut off at the output limit") {
		t.Fatalf("the cut note was not asked for again: %+v", ask[len(ask)-1])
	}
	after := requestMessages(t, r.srv.Requests[4])
	if handover := after[len(after)-1].Content; !strings.HasSuffix(handover, "Handover note:\nNOTE: main.go only declares package main.") {
		t.Fatalf("hand-over:\n%s", handover)
	}
}

// A compaction in a turn with a plan keeps the plan in front of the model:
// the window shows a plan recorded in the context it sends, and the
// narrowing left it behind, while the reminders went on saying "your plan
// is in front of you". It is recorded again, with its marks, in the new
// context.
func TestCompactionKeepsTheTurnsPlan(t *testing.T) {
	t.Parallel()
	r := judgedRig(t, nil, "",
		fake.Turn{ToolCalls: []fake.ToolCall{planWriteCall("w", shapedPlan)}},
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("s", 1, "done", "edited main.go")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("a", "big.txt")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readPath("b", "big.txt")}},
		fake.Turn{Content: "NOTE: step 1 is done; the test is next."},
		fake.Turn{Content: "done"},
	)
	writeTokens(t, r.repo, "big.txt", 1000)
	r.compactAfter(1700)
	if e := r.turn("make main.go print hello"); e != "" {
		t.Fatal(e)
	}
	if n := r.narrowings(); n != 1 {
		t.Fatalf("%d narrowings, want 1", n)
	}
	last := string(r.srv.Requests[len(r.srv.Requests)-1].Raw)
	if !strings.Contains(last, "TestGreeting") || !strings.Contains(last, "edited main.go") {
		t.Fatalf("the request after the compaction lost the plan or its mark:\n%.2000s", last)
	}
}

// A compaction that works clears an earlier one's failure: the failure
// raised the threshold by a quarter until the context grew past it, and
// nothing lowered it again, so with the default window auto-compaction
// never came again in the session.
func TestACompactionClearsAnEarlierFailure(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "", 10,
		fake.Turn{Content: "main.go declares package main."},
		fake.Turn{Content: "NOTE: main.go only declares package main."},
	)
	if err := r.turn("what does main.go do?"); err != "" {
		t.Fatal(err)
	}
	r.eval("(set! compact-gave-up-at 60000)")
	if err := r.turn("/compact"); err != "" {
		t.Fatal(err)
	}
	if got := r.eval("compact-gave-up-at"); got != "0" {
		t.Fatalf("after a compaction that worked: %s", got)
	}
}
