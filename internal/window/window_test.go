package window

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

var pol = Policy{HighWater: 3000, LowWater: 1500, MaxResultTokens: 100000}

// current is p as the current recipe (RecipeVersion) builds with it.
func current(p Policy) Policy {
	p.Thinking, p.InConversation, p.PairTools = true, true, true
	return p
}

// session writes n turns of user -> tool call -> tool result -> answer, with
// tool results of resultSize bytes, and records llm.req events the way the
// loop does (so elision is sticky across calls to Build).
type session struct {
	buf bytes.Buffer
	w   *log.Writer
	n   int
}

func newSession() *session {
	s := &session{}
	s.w = log.New(&s.buf, "s")
	return s
}

func (s *session) turn(resultSize int) {
	i := s.n
	s.n++
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: fmt.Sprintf("question %d", i)})
	s.w.Append(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: fmt.Sprint("c", i), Name: "fs-read", Args: json.RawMessage(`{"path":"x"}`)}}})
	s.w.Append(log.KindToolResult, log.ToolResult{ID: fmt.Sprint("c", i), Name: "fs-read", OK: true, Value: strings.Repeat("x", resultSize)})
	s.w.Append(log.KindLLMResp, log.LLMResp{Content: fmt.Sprintf("answer %d", i)})
}

func (s *session) events(t *testing.T) []log.Event {
	t.Helper()
	evs, err := log.Read(bytes.NewReader(s.buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// build runs Build and records llm.req like the loop does.
func (s *session) build(t *testing.T, p Policy) Result {
	t.Helper()
	r := Build(s.events(t), "sys", 10, p)
	s.w.Append(log.KindLLMReq, log.LLMReq{Messages: len(r.Messages), EstTokens: r.EstTokens, Elided: r.Elided})
	return r
}

func TestUnderHighWaterNothingChanges(t *testing.T) {
	s := newSession()
	s.turn(30)
	s.turn(30)
	r := s.build(t, pol)
	if len(r.Messages) != 1+2*4 || len(r.Elided) != 0 || r.Trimmed {
		t.Fatalf("messages=%d elided=%v trimmed=%v", len(r.Messages), r.Elided, r.Trimmed)
	}
	if r.Messages[0].Role != llm.RoleSystem || !strings.Contains(r.Messages[0].Content, "Current task") || !strings.HasSuffix(r.Messages[0].Content, "question 1") {
		t.Fatalf("task block: %q", r.Messages[0].Content)
	}
	if r.Messages[3].Role != llm.RoleTool || r.Messages[3].ToolCallID != "c0" {
		t.Fatalf("shape: %+v", r.Messages)
	}
}

func TestHysteresisTrimsToLowWaterAndThenHolds(t *testing.T) {
	s := newSession()
	for i := 0; i < 6; i++ {
		s.turn(1500) // ~500 tokens each result
	}
	r := s.build(t, pol)
	if !r.Trimmed || r.EstTokens > pol.LowWater {
		t.Fatalf("trim: trimmed=%v est=%d", r.Trimmed, r.EstTokens)
	}
	first := r.Elided
	// Small growth: no new elision, identical prefix.
	s.turn(30)
	r2 := s.build(t, pol)
	if r2.Trimmed {
		t.Fatal("trimmed again while under high water")
	}
	if len(r2.Elided) != len(first) {
		t.Fatalf("elision changed between trims: %v -> %v", first, r2.Elided)
	}
	for i := range r.Messages {
		if i == 0 {
			continue // the task block changes with the new user message
		}
		if r.Messages[i].Content != r2.Messages[i].Content {
			t.Fatalf("prefix changed at %d", i)
		}
	}
}

func TestElisionIsStickyAcrossBuilds(t *testing.T) {
	s := newSession()
	for i := 0; i < 6; i++ {
		s.turn(1500)
	}
	r := s.build(t, pol)
	if len(r.Elided) == 0 {
		t.Fatal("expected elision")
	}
	// Even with an enormous budget, previously elided seqs stay elided.
	big := Policy{HighWater: 1 << 30, LowWater: 1 << 29, MaxResultTokens: 1 << 20}
	r2 := s.build(t, big)
	if len(r2.Elided) != len(r.Elided) {
		t.Fatalf("sticky: %v vs %v", r.Elided, r2.Elided)
	}
	for _, m := range r2.Messages {
		for _, seq := range r.Elided {
			if m.ToolCallID == fmt.Sprint("c", seqToTurn(seq)) && !strings.HasPrefix(m.Content, "[elided") {
				t.Fatalf("seq %d came back", seq)
			}
		}
	}
}

func seqToTurn(seq uint64) int { return int((seq - 1) / 4) }

func TestUserMessagesAreNeverElidedToolsGoFirst(t *testing.T) {
	s := newSession()
	for i := 0; i < 8; i++ {
		s.turn(600)
	}
	// Make the answers big too so pass 2 has to run.
	evs := s.events(t)
	for i := range evs {
		if evs[i].Kind == log.KindLLMResp {
			var p log.LLMResp
			evs[i].Decode(&p)
			if p.Content != "" {
				p.Content = strings.Repeat("a", 1500)
				evs[i].Payload, _ = json.Marshal(p)
			}
		}
	}
	r := Build(evs, "sys", 0, Policy{HighWater: 2000, LowWater: 800, MaxResultTokens: 100000})
	for _, m := range r.Messages {
		if m.Role == llm.RoleUser && strings.HasPrefix(m.Content, "[elided") {
			t.Fatal("a user message was elided")
		}
	}
	// Every non-protected tool result is elided before any assistant turn is.
	firstAssistantElided, lastToolKept := -1, -1
	for i, m := range r.Messages {
		if i >= len(r.Messages)-Keep {
			break
		}
		el := strings.HasPrefix(m.Content, "[elided")
		if m.Role == llm.RoleAssistant && el && firstAssistantElided < 0 {
			firstAssistantElided = i
		}
		if m.Role == llm.RoleTool && !el {
			lastToolKept = i
		}
	}
	if firstAssistantElided >= 0 && lastToolKept >= 0 {
		t.Fatalf("assistant turn elided at %d while a tool result survived at %d", firstAssistantElided, lastToolKept)
	}
}

func TestResultCapIsHeadTailAndDeterministic(t *testing.T) {
	s := newSession()
	s.turn(9000) // ~3000 tokens
	r := Build(s.events(t), "sys", 0, Policy{HighWater: 1 << 20, LowWater: 1 << 19, MaxResultTokens: 300})
	m := r.Messages[3]
	if !strings.Contains(m.Content, "tokens omitted") || Estimate(m.Content) > 400 {
		t.Fatalf("cap: %d tokens, %q", Estimate(m.Content), m.Content[:60])
	}
	if !strings.HasPrefix(m.Content, "xxx") || !strings.HasSuffix(m.Content, "xxx") {
		t.Fatal("head and tail should be kept")
	}
	r2 := Build(s.events(t), "sys", 0, Policy{HighWater: 1 << 20, LowWater: 1 << 19, MaxResultTokens: 300})
	if r2.Messages[3].Content != m.Content {
		t.Fatal("cap must be deterministic")
	}
}

// From recipe v17 a result too large to show is cut at line ends, and the
// marker names the lines left out: of the file, for an fs-read, as the
// fs-read that brings them back (whole files or a ranged read's header);
// of the result otherwise. Older recipes keep the old marker.
func TestNamedCutsSayWhichLinesAreLeftOut(t *testing.T) {
	var file strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&file, "line %03d of the file\n", i)
	}
	s := newSession()
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "read it"})
	s.w.Append(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: "a", Name: "fs-read"}, {ID: "b", Name: "fs-read"}, {ID: "c", Name: "shell-exec"}}})
	s.w.Append(log.KindToolResult, log.ToolResult{ID: "a", Name: "fs-read", OK: true, Value: file.String()})
	s.w.Append(log.KindToolResult, log.ToolResult{ID: "b", Name: "fs-read", OK: true, Value: "[lines 101-400 of 400]\n" + strings.Join(strings.SplitAfter(file.String(), "\n")[100:], "")})
	s.w.Append(log.KindToolResult, log.ToolResult{ID: "c", Name: "shell-exec", OK: true, Value: file.String()})
	named := Build(s.events(t), "sys", 0, Policy{HighWater: 1 << 20, LowWater: 1 << 19, MaxResultTokens: 1000, NamedCuts: true}).Messages
	// The system prompt, the request, the calls, then the three results.
	whole, ranged, shell := named[3].Content, named[4].Content, named[5].Content
	for _, tc := range []struct{ name, got, marker, before, after string }{
		// 21-byte lines, a 2,000-byte head and a 1,000-byte tail, cut at
		// the line ends inside them.
		{"whole file", whole, "[... lines 96-353 of the file left out here, about 1806 tokens: read them with fs-read, start 96, count 258 ...]\n", "line 095 of the file\n", "line 354 of the file\n"},
		{"ranged read", ranged, "[... lines 195-353 of the file left out here, about 1113 tokens: read them with fs-read, start 195, count 159 ...]\n", "line 194 of the file\n", "line 354 of the file\n"},
		{"command output", shell, "[... lines 96-353 of this result left out here, about 1806 tokens: narrow the command, or page through its output, for them ...]\n", "line 095 of the file\n", "line 354 of the file\n"},
	} {
		if !strings.Contains(tc.got, tc.before+tc.marker+tc.after) {
			t.Errorf("%s: no %q between the lines around it:\n%s", tc.name, tc.marker, tc.got)
		}
	}
	old := Build(s.events(t), "sys", 0, Policy{HighWater: 1 << 20, LowWater: 1 << 19, MaxResultTokens: 1000}).Messages
	if !strings.Contains(old[3].Content, "tokens omitted; re-read the file") {
		t.Errorf("recipe v16 changed its marker:\n%s", old[3].Content)
	}
	// One long line has no line end to cut at: the old marker stands.
	long := capNamed(strings.Repeat("x", 9000), 1000, "fs-read")
	if !strings.Contains(long, "tokens omitted") {
		t.Errorf("one long line: %q", long[:80])
	}
}

func TestErrorResultsBecomeJSON(t *testing.T) {
	var buf bytes.Buffer
	w := log.New(&buf, "s")
	w.Append(log.KindToolResult, log.ToolResult{ID: "c", Name: "fs-read", OK: false, Error: "fs-read: denied: nope"})
	evs, _ := log.Read(&buf)
	r := Build(evs, "sys", 0, pol)
	if r.Messages[1].Content != `{"error":"fs-read: denied: nope"}` {
		t.Fatalf("%q", r.Messages[1].Content)
	}
}

// A narrowed context is only what follows the narrowing, under the
// strategy's system prompt; widen or the user's next message ends it.
func TestNarrowedContext(t *testing.T) {
	s := newSession()
	s.turn(10)
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "build the thing"})
	s.w.Append(log.KindLLMResp, log.LLMResp{Content: "plan: 1. write it"})
	s.w.Append(log.KindContextNarrow, log.ContextNarrow{System: "You implement a plan."})
	s.w.Append(log.KindHarnessMsg, log.HarnessMsg{Text: "Plan: 1. write it"})
	s.w.Append(log.KindLLMResp, log.LLMResp{Content: "written"})

	narrowed := Build(s.events(t), "SESSION PROMPT", 0, pol).Messages
	if len(narrowed) != 3 || narrowed[0].Content != "You implement a plan." ||
		narrowed[1].Content != "Plan: 1. write it" || narrowed[2].Content != "written" {
		t.Fatalf("narrowed window: %+v", narrowed)
	}

	s.w.Append(log.KindContextWiden, log.ContextWiden{})
	wide := Build(s.events(t), "SESSION PROMPT", 0, pol).Messages
	if !strings.HasPrefix(wide[0].Content, "SESSION PROMPT") || !strings.Contains(wide[0].Content, "build the thing") {
		t.Fatalf("widened system prompt: %q", wide[0].Content)
	}
	if len(wide) != 1+4+4 { // system, the first turn, then this turn with what happened while narrowed
		t.Fatalf("widened window has %d messages: %+v", len(wide), wide)
	}

	// Narrowing never outlives the turn: the next user message widens.
	s.w.Append(log.KindContextNarrow, log.ContextNarrow{System: "side task"})
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "next"})
	if next := Build(s.events(t), "SESSION PROMPT", 0, pol).Messages; !strings.HasPrefix(next[0].Content, "SESSION PROMPT") {
		t.Fatalf("a new turn stayed narrowed: %q", next[0].Content)
	}
}

// Contexts nest: a fold drops the innermost one, its messages included, and
// the window is the enclosing context again.
func TestFoldedContext(t *testing.T) {
	s := newSession()
	s.turn(10)
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "build the thing"})
	s.w.Append(log.KindLLMResp, log.LLMResp{Content: "working"})
	s.w.Append(log.KindContextNarrow, log.ContextNarrow{System: "compacted"})
	s.w.Append(log.KindHarnessMsg, log.HarnessMsg{Text: "handover"})
	s.w.Append(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: "x", Name: "ask-expert", Args: json.RawMessage(`{}`)}}})
	s.w.Append(log.KindContextNarrow, log.ContextNarrow{System: "You are an expert."})
	s.w.Append(log.KindHarnessMsg, log.HarnessMsg{Text: "the question"})
	s.w.Append(log.KindLLMResp, log.LLMResp{Content: "the answer"})

	inner := Build(s.events(t), "SESSION PROMPT", 0, pol).Messages
	if len(inner) != 3 || inner[0].Content != "You are an expert." || inner[1].Content != "the question" {
		t.Fatalf("inner context: %+v", inner)
	}

	s.w.Append(log.KindContextFold, log.ContextFold{})
	s.w.Append(log.KindToolResult, log.ToolResult{ID: "x", Name: "ask-expert", OK: true, Value: "the answer"})
	outer := Build(s.events(t), "SESSION PROMPT", 0, pol).Messages
	if len(outer) != 4 || outer[0].Content != "compacted" || outer[1].Content != "handover" ||
		len(outer[2].ToolCalls) != 1 || outer[3].Role != llm.RoleTool || outer[3].Content != "the answer" {
		t.Fatalf("after the fold: %+v", outer)
	}

	// Folding the outer context too hides all of it (widen would keep it):
	// the whole conversation again, up to where the first narrowing began.
	s.w.Append(log.KindContextFold, log.ContextFold{})
	wide := Build(s.events(t), "SESSION PROMPT", 0, pol).Messages
	for _, m := range wide {
		if m.Content == "the question" || m.Content == "handover" {
			t.Fatalf("a folded context came back: %+v", wide)
		}
	}
	if !strings.HasPrefix(wide[0].Content, "SESSION PROMPT") || wide[len(wide)-1].Content != "working" {
		t.Fatalf("whole conversation after folding everything: %+v", wide)
	}
}

// A lasting narrowing, such as a compaction's, outlasts its turn: from the
// user's next message the conversation starts there, under the session's
// system prompt with the new task. Within its turn a fold or widen ends it
// like any other narrowing.
func TestLastingNarrowing(t *testing.T) {
	s := newSession()
	s.turn(10)
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "/compact"})
	s.w.Append(log.KindHarnessMsg, log.HarnessMsg{Text: "write a note"})
	s.w.Append(log.KindLLMResp, log.LLMResp{Content: "the note"})
	s.w.Append(log.KindContextNarrow, log.ContextNarrow{System: "SESSION PROMPT", Agent: "compaction", Lasting: true})
	s.w.Append(log.KindHarnessMsg, log.HarnessMsg{Text: "handover"})

	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "next"})
	next := Build(s.events(t), "SESSION PROMPT", 0, pol).Messages
	if len(next) != 3 || next[0].Content != "SESSION PROMPT\n\n"+DefaultTaskHeader+"\nnext" ||
		next[1].Content != "handover" || next[2].Content != "next" {
		t.Fatalf("the turn after a lasting narrowing: %+v", next)
	}

	// It lasts through later turns; a narrowing that does not last still
	// ends at the next message.
	s.w.Append(log.KindLLMResp, log.LLMResp{Content: "answer"})
	s.w.Append(log.KindContextNarrow, log.ContextNarrow{System: "side task"})
	s.w.Append(log.KindHarnessMsg, log.HarnessMsg{Text: "side work"})
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "again"})
	again := Build(s.events(t), "SESSION PROMPT", 0, pol).Messages
	if len(again) != 6 || again[1].Content != "handover" || again[4].Content != "side work" || again[5].Content != "again" {
		t.Fatalf("two turns after a lasting narrowing: %+v", again)
	}

	// Folded or widened in its own turn, a lasting narrowing is gone by the
	// next message.
	s.w.Append(log.KindContextNarrow, log.ContextNarrow{System: "folded", Lasting: true})
	s.w.Append(log.KindHarnessMsg, log.HarnessMsg{Text: "folded away"})
	s.w.Append(log.KindContextFold, log.ContextFold{})
	s.w.Append(log.KindContextNarrow, log.ContextNarrow{System: "widened", Lasting: true})
	s.w.Append(log.KindHarnessMsg, log.HarnessMsg{Text: "widened again"})
	s.w.Append(log.KindContextWiden, log.ContextWiden{})
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "last"})
	last := Build(s.events(t), "SESSION PROMPT", 0, pol).Messages
	if len(last) != 8 || last[1].Content != "handover" || last[6].Content != "widened again" || last[7].Content != "last" {
		t.Fatalf("after a folded and a widened lasting narrowing: %+v", last)
	}
	for _, m := range last {
		if m.Content == "folded away" || m.Content == "answer 0" || m.Content == "the note" {
			t.Fatalf("the window reached past the compaction or into a folded context: %+v", last)
		}
	}
}

// TokensFrom is the window from a message to its end: the system prompt and
// the tools are before it, and what follows it is counted.
func TestTokensFrom(t *testing.T) {
	s := newSession()
	s.turn(300)
	s.turn(600)
	evs := s.events(t)
	r := Build(evs, "sys", 10, pol)
	var results []uint64
	for _, ev := range evs {
		if ev.Kind == log.KindToolResult {
			results = append(results, ev.Seq)
		}
	}
	first, ok := r.TokensFrom(results[0])
	second, ok2 := r.TokensFrom(results[1])
	if !ok || !ok2 {
		t.Fatal("a result in the window was not found")
	}
	if first >= r.EstTokens-Estimate("sys")-10 || second >= first || second < Estimate(strings.Repeat("x", 600)) {
		t.Fatalf("from the first result %d, from the second %d, of %d", first, second, r.EstTokens)
	}
	if _, ok := r.TokensFrom(9999); ok {
		t.Fatal("found a seq the window does not show")
	}
}

// A run's later turn carries its plan with the steps already marked: the
// system prompt shows them under the plan, and only for that turn.
func TestCarriedPlanShowsItsMarks(t *testing.T) {
	s := newSession()
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "run turn 1"})
	s.w.Append(log.KindGatePlan, log.GatePlan{Noul: "required", Plan: "1. read\n2. edit"})
	if sys := Build(s.events(t), "sys", 0, pol).Messages[0].Content; !strings.HasSuffix(sys, "1. read\n2. edit") {
		t.Fatalf("a drafted plan: %q", sys)
	}
	s.w.Append(log.KindPlanStep, log.PlanStep{Step: 1, Of: 2, State: "done", Note: "read it"})
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "run turn 2"})
	s.w.Append(log.KindGatePlan, log.GatePlan{Noul: "carried", Plan: "1. read\n2. edit", Marks: []log.PlanStep{{Step: 1, Of: 2, State: "done", Note: "read it"}}})
	sys := Build(s.events(t), "sys", 0, pol).Messages[0].Content
	if !strings.HasSuffix(sys, "1. read\n2. edit\n\nMarked in earlier turns of this task:\nStep 1 done: read it") {
		t.Fatalf("a carried plan: %q", sys)
	}
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "something else"})
	if sys := Build(s.events(t), "sys", 0, pol).Messages[0].Content; strings.Contains(sys, "Marked in earlier turns") {
		t.Fatalf("marks outlived their turn: %q", sys)
	}
}

// From recipe v14 a reply carries the thinking it came with, and counts
// it; trimming takes the thinking out with the reply. Without the policy,
// as for an older recipe, replies carry none.
func TestRepliesCarryTheirThinking(t *testing.T) {
	s := newSession()
	for i := range 3 {
		s.w.Append(log.KindUserMsg, log.UserMsg{Text: fmt.Sprintf("question %d", i)})
		s.w.Append(log.KindLLMResp, log.LLMResp{Thinking: fmt.Sprintf("thought %d ", i) + strings.Repeat("t", 3000),
			ToolCalls: []log.ToolCall{{ID: fmt.Sprint("c", i), Name: "fs-read", Args: json.RawMessage(`{"path":"x"}`)}}})
		s.w.Append(log.KindToolResult, log.ToolResult{ID: fmt.Sprint("c", i), Name: "fs-read", OK: true, Value: "x"})
		s.w.Append(log.KindLLMResp, log.LLMResp{Content: fmt.Sprintf("answer %d", i), Thinking: "done"})
	}
	roomy := Policy{HighWater: 100000, LowWater: 50000, MaxResultTokens: 100000}
	without := Build(s.events(t), "sys", 10, roomy)
	roomy.Thinking = true
	with := Build(s.events(t), "sys", 10, roomy)
	for _, m := range without.Messages {
		if m.Thinking != "" {
			t.Fatalf("thinking without the policy: %+v", m)
		}
	}
	if !strings.HasPrefix(with.Messages[2].Thinking, "thought 0 ") || with.Messages[4].Thinking != "done" {
		t.Fatalf("replies without their thinking: %+v", with.Messages)
	}
	if with.EstTokens-without.EstTokens != 3*(Estimate("thought 0 "+strings.Repeat("t", 3000))+Estimate("done")) {
		t.Fatalf("thinking not counted: %d with, %d without", with.EstTokens, without.EstTokens)
	}
	tight := Policy{HighWater: 2000, LowWater: 1000, MaxResultTokens: 100000, Thinking: true}
	trimmed := Build(s.events(t), "sys", 10, tight)
	if !trimmed.Trimmed || trimmed.Messages[2].Content != "[elided seq=2]" || trimmed.Messages[2].Thinking != "" {
		t.Fatalf("an elided reply kept its thinking: %+v", trimmed.Messages[2])
	}
}

// From recipe v15 the system prompt never changes: the user's request is
// their message, under the task header, and a plan is a message of its
// own, under the plan header. A plan-write is recorded before its call is
// answered, so its plan follows the batch's results, a sub-agent's
// included, and the sub-agent does not see it. Each window then begins
// with the one before it.
func TestRequestAndPlanJoinTheConversation(t *testing.T) {
	p := current(pol)
	s := newSession()
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "change it"})
	first := Build(s.events(t), "sys", 0, p).Messages
	if len(first) != 2 || first[0].Content != "sys" || first[1].Content != DefaultTaskHeader+"\nchange it" {
		t.Fatalf("the request: %+v", first)
	}

	s.w.Append(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{
		{ID: "w", Name: "plan-write", Args: json.RawMessage(`{}`)},
		{ID: "x", Name: "ask-expert", Args: json.RawMessage(`{}`)}}})
	s.w.Append(log.KindGatePlan, log.GatePlan{Noul: "model", Plan: "1. edit"})
	s.w.Append(log.KindToolResult, log.ToolResult{ID: "w", Name: "plan-write", OK: true, Value: "Plan recorded"})
	s.w.Append(log.KindContextNarrow, log.ContextNarrow{System: "You are an expert."})
	s.w.Append(log.KindHarnessMsg, log.HarnessMsg{Text: "the question"})
	if inner := Build(s.events(t), "sys", 0, p).Messages; len(inner) != 2 || inner[1].Content != "the question" {
		t.Fatalf("the sub-agent's context: %+v", inner)
	}
	s.w.Append(log.KindLLMResp, log.LLMResp{Content: "the answer"})
	s.w.Append(log.KindContextFold, log.ContextFold{})
	s.w.Append(log.KindToolResult, log.ToolResult{ID: "x", Name: "ask-expert", OK: true, Value: "the answer"})
	planned := Build(s.events(t), "sys", 0, p).Messages
	if len(planned) != 6 || planned[3].ToolCallID != "w" || planned[4].ToolCallID != "x" ||
		planned[5].Role != llm.RoleUser || planned[5].Content != DefaultPlanHeader+"\n1. edit" {
		t.Fatalf("the plan after the batch: %+v", planned)
	}

	s.w.Append(log.KindLLMResp, log.LLMResp{Content: "edited"})
	s.w.Append(log.KindPlanStep, log.PlanStep{Step: 1, Of: 1, State: "done"})
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "go on"})
	s.w.Append(log.KindGatePlan, log.GatePlan{Noul: "carried", Plan: "1. edit", Marks: []log.PlanStep{{Step: 1, Of: 1, State: "done", Note: "edited"}}})
	next := Build(s.events(t), "sys", 0, p).Messages
	if n := len(next); n != 9 || next[7].Content != DefaultTaskHeader+"\ngo on" ||
		next[8].Content != DefaultPlanHeader+"\n1. edit\n\nMarked in earlier turns of this task:\nStep 1 done: edited" {
		t.Fatalf("the next turn: %+v", next)
	}
	for _, w := range [][]llm.Message{first, planned} {
		for i := range w {
			if !reflect.DeepEqual(w[i], next[i]) {
				t.Fatalf("message %d changed: %+v became %+v", i, w[i], next[i])
			}
		}
	}
}
