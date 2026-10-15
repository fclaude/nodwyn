package core

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/retrieval"
	"github.com/fclaude/nodwyn/internal/scheme"
	"github.com/fclaude/nodwyn/internal/window"
)

// What a model sees of a long conversation shrinks: the window elides old
// replies and results ([elided seq=N]), strategies fold results to an
// outline, shell output keeps its ends, and a compaction leaves a note. The
// log keeps all of it. index-search ranks the conversation's events by
// their words and recall brings one back as the log recorded it (ADR 50).
const (
	searchLines   = 3   // lines shown of each event found
	searchLineCut = 200 // characters shown of each line
	callCut       = 300 // characters of a call's arguments in a heading
)

// conversation is the session's conversation as index-search and recall
// read it: the events a model could have seen, as text, in a word index.
// It is built from the log at the first search or recall (a resumed
// session holds only the end of its log, and History reads the rest) and
// extended with what was recorded since. It is kept in memory and never
// written: it is derived from the log (SPEC, invariant 1).
type conversation struct {
	mu      sync.Mutex
	built   bool
	through uint64 // the last seq read
	index   retrieval.Index
	found   []convEvent    // what index holds, by document number
	bySeq   map[uint64]int // a seq's document number
	calls   map[string]log.ToolCall
	agents  []string // the open narrowings' agents, innermost last
}

// convEvent is one event as the conversation reads it.
type convEvent struct {
	seq   uint64
	what  string // "the person's message", "the result of fs-read {…}"
	agent string // the agent whose narrowed context holds it; "" for none
	text  string
	call  string         // the call a result answers
	calls []log.ToolCall // the calls a reply made
}

// heading names an event as search results and recall show it.
func (e convEvent) heading() string {
	h := fmt.Sprintf("seq %d, %s", e.seq, e.what)
	if e.agent != "" {
		h += ", in the " + e.agent + "'s context"
	}
	return h
}

// update reads the events recorded since the conversation was last read.
func (c *conversation) update(s *Session) error {
	var events []log.Event
	if !c.built {
		all, err := s.History()
		if err != nil {
			return err
		}
		events, c.built = all, true
		c.bySeq, c.calls = map[uint64]int{}, map[string]log.ToolCall{}
	} else {
		events = s.EventsSoFar()
	}
	for _, ev := range events {
		if ev.Seq > c.through {
			c.through = ev.Seq
			c.add(ev)
		}
	}
	return nil
}

// add indexes one event, if a model could have seen it. Records of the
// guard, of requests and of notes are not the conversation, and neither
// is thinking. Searches and recalls are left out: their results repeat
// other events, and a search would find its own call.
func (c *conversation) add(ev log.Event) {
	agent := ""
	if n := len(c.agents); n > 0 {
		agent = c.agents[n-1]
	}
	var e convEvent
	var fields []retrieval.Field
	switch ev.Kind {
	case log.KindContextNarrow:
		var p log.ContextNarrow
		if ev.Decode(&p) == nil {
			// A lasting narrowing, a compaction's, is the conversation
			// going on, not an agent's context.
			name := p.Agent
			if p.Lasting {
				name = agent
			}
			c.agents = append(c.agents, name)
		}
		return
	case log.KindContextFold:
		if n := len(c.agents); n > 0 {
			c.agents = c.agents[:n-1]
		}
		return
	case log.KindContextWiden:
		c.agents = nil
		return
	case log.KindToolCall:
		var p log.ToolCallEvent
		if ev.Decode(&p) == nil {
			c.calls[p.ID] = p.ToolCall
		}
		return
	case log.KindUserMsg:
		var p log.UserMsg
		if ev.Decode(&p) != nil {
			return
		}
		// A person's message ends every narrowing still open (window.Build).
		c.agents, agent = nil, ""
		e = convEvent{what: "the person's message", text: p.Text}
	case log.KindHarnessMsg:
		var p log.HarnessMsg
		if ev.Decode(&p) != nil {
			return
		}
		e = convEvent{what: "nodwyn's message", text: p.Text}
	case log.KindAttach:
		var p log.Attach
		if ev.Decode(&p) != nil {
			return
		}
		e = convEvent{what: "the attached file " + p.Name, text: window.AttachmentText(p)}
	case log.KindGatePlan:
		var p log.GatePlan
		if ev.Decode(&p) != nil || p.Plan == "" {
			return
		}
		e = convEvent{what: "the plan", text: p.Plan}
	case log.KindSummary:
		var p log.Summary
		if ev.Decode(&p) != nil {
			return
		}
		e = convEvent{what: fmt.Sprintf("the summary of seq %d to %d", p.From, p.Through), text: p.Text}
	case log.KindLLMResp:
		var p log.LLMResp
		if ev.Decode(&p) != nil {
			return
		}
		var lines []string
		if p.Content != "" {
			lines = append(lines, p.Content)
		}
		for _, tc := range p.ToolCalls {
			// The window pairs a result with its reply's call; a log may
			// have no tool.call record of it.
			c.calls[tc.ID] = tc
			if !readsTheConversation(tc.Name) {
				e.calls = append(e.calls, tc)
				lines = append(lines, tc.Name+" "+string(tc.Args))
			}
		}
		if len(lines) == 0 {
			return
		}
		e.what, e.text = "a reply", strings.Join(lines, "\n")
	case log.KindToolResult:
		var p log.ToolResult
		if ev.Decode(&p) != nil || readsTheConversation(p.Name) {
			return
		}
		call, ok := c.calls[p.ID]
		if !ok {
			call = log.ToolCall{ID: p.ID, Name: p.Name}
		}
		e = convEvent{what: "the result of " + callLine(call), text: resultText(p.Value), call: p.ID}
		if !p.OK {
			e.what, e.text = e.what+", which failed", p.Error
		}
		// What was asked names a result better than its output does.
		fields = []retrieval.Field{{Text: call.Name + " " + string(call.Args), Weight: 3}}
	default:
		return
	}
	e.seq, e.agent = ev.Seq, agent
	c.bySeq[ev.Seq] = c.index.Add(append(fields, retrieval.Field{Text: e.text})...)
	c.found = append(c.found, e)
}

func readsTheConversation(tool string) bool { return tool == "index-search" || tool == "recall" }

// resultText is a tool result as text with lines, so recall can page it.
// A result is recorded as its tool returned it, and a shell's or a
// search's is one line of JSON. A shell result reads as the model read it
// (shell.scm's shell-plain: the exit status, the output, then [stderr]
// and the errors), but whole; other JSON is indented; anything else is as
// recorded.
func resultText(value string) string {
	var shell struct {
		Status *json.Number `json:"status"`
		Stdout *string      `json:"stdout"`
		Stderr *string      `json:"stderr"`
	}
	d := json.NewDecoder(strings.NewReader(value))
	d.UseNumber()
	if d.Decode(&shell) == nil && shell.Status != nil && (shell.Stdout != nil || shell.Stderr != nil) {
		text := "exit " + shell.Status.String()
		if shell.Stdout != nil && *shell.Stdout != "" {
			text += "\n" + *shell.Stdout
		}
		if shell.Stderr != nil && *shell.Stderr != "" {
			text += "\n[stderr]\n" + *shell.Stderr
		}
		return text
	}
	if t := strings.TrimSpace(value); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		var b bytes.Buffer
		if json.Indent(&b, []byte(t), "", "  ") == nil {
			return b.String()
		}
	}
	return value
}

// callLine is a call as a heading shows it: its name and its arguments,
// cut.
func callLine(tc log.ToolCall) string {
	args := string(tc.Args)
	if args == "" || args == "null" {
		return tc.Name
	}
	return tc.Name + " " + cut(args, callCut)
}

// cut keeps the first n characters of s, marking a cut with an ellipsis.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// searchConversation is index-search's "conversation" scope.
func (s *Session) searchConversation(_ context.Context, query string, limit int) (string, error) {
	return s.conversation.search(s, query, limit)
}

// search returns the events that best match query, each under its heading
// with its lines that hold the most of the words matched, numbered as
// recall numbers them.
func (c *conversation) search(s *Session, query string, limit int) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.update(s); err != nil {
		return "", err
	}
	hits := c.index.Search(query, limit, nil)
	if len(hits) == 0 {
		return fmt.Sprintf("No event of this conversation holds any of these words (%d events searched).", c.index.Len()), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[the %d best of %d events, best first; recall brings one back whole by its seq]\n", len(hits), c.index.Len())
	for _, h := range hits {
		e := c.found[h.Doc]
		b.WriteString(e.heading() + "\n")
		for _, l := range bestLines(e.text, h.Matched) {
			fmt.Fprintf(&b, "  %d: %s\n", l.n, cut(l.text, searchLineCut))
		}
	}
	return b.String(), nil
}

type numbered struct {
	n    int
	text string
}

// bestLines are the lines of text holding the most of words, at most
// searchLines of them, in their order; the first line when none does (the
// words were in a result's call).
func bestLines(text string, words []string) []numbered {
	type scored struct {
		numbered
		score int
	}
	var lines []scored
	for i, line := range strings.Split(text, "\n") {
		score := 0
		has := retrieval.Words(line)
		for _, w := range words {
			if slices.Contains(has, w) {
				score++
			}
		}
		lines = append(lines, scored{numbered{i + 1, strings.TrimRight(line, "\r")}, score})
	}
	best := slices.Clone(lines)
	slices.SortStableFunc(best, func(p, q scored) int { return cmp.Compare(q.score, p.score) })
	var out []numbered
	for _, l := range best[:min(searchLines, len(best))] {
		if l.score > 0 {
			out = append(out, l.numbered)
		}
	}
	if len(out) == 0 && strings.TrimSpace(text) != "" {
		return []numbered{lines[0].numbered}
	}
	slices.SortFunc(out, func(p, q numbered) int { return cmp.Compare(p.n, q.n) })
	return out
}

// recall returns the event at seq as the log recorded it, under its
// heading: lines start to start+count-1 of it (count 0 is as many as
// fit), at most budget bytes, as a ranged fs-read returns a file's.
func (c *conversation) recall(s *Session, seq uint64, start, count int64, budget int) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.update(s); err != nil {
		return "", err
	}
	doc, ok := c.bySeq[seq]
	switch {
	case !ok && seq > c.through:
		return "", fmt.Errorf("there is no seq %d: this conversation's last is %d", seq, c.through)
	case !ok:
		return "", fmt.Errorf("seq %d is not a message, a reply or a tool result: recall brings back those, by the N of [elided seq=N] or a seq index-search gave", seq)
	}
	e := c.found[doc]
	text := e.text
	for _, tc := range e.calls {
		// A reply's calls say where their results are, which an elided
		// reply's window no longer shows.
		if r := c.resultOf(doc, tc.ID); r != 0 {
			text += fmt.Sprintf("\n[the result of %s is seq %d]", tc.Name, r)
		} else {
			text += fmt.Sprintf("\n[%s has no result recorded]", tc.Name)
		}
	}
	head := "[" + e.heading() + "]\n"
	if text == "" {
		return head + "(empty)", nil
	}
	if count == 0 {
		count = 1 << 40
	}
	body, err := prim.ReadLines(strings.NewReader(text), fmt.Sprintf("seq %d", seq), start, count, budget-len(head))
	if err != nil {
		return "", err
	}
	return head + body, nil
}

// resultOf is the seq of the first result after document doc that
// answers the call id, 0 when none does.
func (c *conversation) resultOf(doc int, id string) uint64 {
	for _, e := range c.found[doc+1:] {
		if e.call == id {
			return e.seq
		}
	}
	return 0
}

// registerRecall adds recall, offered to the model as a tool. It reads the
// log, never a file, so its values carry the log's provenance
// (external:log), as session-events' do. index-search, whose conversation
// scope is searchConversation, is prim's (internal/prim/repoindex.go).
func (s *Session) registerRecall() error {
	for _, p := range []*scheme.Prim{
		{
			Name: "recall",
			Meta: scheme.Meta{
				Doc: "Bring back one event of this task's conversation as it was recorded: a message, a reply with its tool calls, or a tool result, by its seq (the N of [elided seq=N], or a seq index-search gave). It comes back whole, a long one in lines as a ranged fs-read reads a file, so a result that was elided, folded or cut can be read again without running its command. It shows what was then, not what is now: read a file again with fs-read.",
				Params: []scheme.Param{
					{Name: "seq", Type: "integer", Doc: "the event's seq"},
					{Name: "start", Type: "integer", Doc: "the 1-based line to start at; 1 unless given", Optional: true},
					{Name: "count", Type: "integer", Doc: "how many lines to return; as many as fit unless given", Optional: true},
				},
				Host:        true,
				Origin:      "builtin",
				TaintSource: "external:log",
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				seq, err := c.Int(0)
				if err != nil {
					return scheme.Void, err
				}
				if seq < 1 {
					return scheme.Void, fmt.Errorf("seq must be positive, got %d", seq)
				}
				var start, count int64
				if len(c.Args) > 1 {
					if start, err = c.Int(1); err != nil {
						return scheme.Void, err
					}
				}
				if len(c.Args) > 2 {
					if count, err = c.Int(2); err != nil {
						return scheme.Void, err
					}
				}
				out, err := s.conversation.recall(s, uint64(seq), start, count, s.rangeBytes())
				if err != nil {
					return scheme.Void, err
				}
				return scheme.Str(out), nil
			},
		},
	} {
		if err := s.interp.Register(p); err != nil {
			return err
		}
	}
	return nil
}
