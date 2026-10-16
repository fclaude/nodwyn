package core

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/memory"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/retrieval"
	"github.com/fclaude/nodwyn/internal/scheme"
	"github.com/fclaude/nodwyn/internal/window"
)

// What a model sees of a long conversation shrinks: the window elides old
// replies and results ([elided seq=N]), strategies fold results to an
// outline, shell output keeps its ends, and a compaction leaves a note. The
// log keeps all of it. index-search ranks the conversation's events by
// their words, showing each under the turn that holds it, and recall
// brings one back as the log recorded it (ADR 50). The words are a log's
// memory, kept beside it (memory.go, ADR 51).
const (
	searchLines   = 3   // lines shown of each event found
	searchLineCut = 200 // characters shown of each line
	callCut       = 300 // characters of a call's arguments in a heading
	askedCut      = 160 // characters of a request in a turn's line
)

// convDoc is one event as index-search ranks it and recall reads it: what
// it is, the agent whose narrowed context holds it ("" for none), the
// call a result answers and the calls a reply made. Its text is read from
// the log again when it is shown.
type convDoc struct {
	Seq   uint64    `json:"-"`
	What  string    `json:"what"` // "the person's message", "the result of fs-read {…}"
	Agent string    `json:"agent,omitempty"`
	Call  string    `json:"call,omitempty"`
	Calls []callRef `json:"calls,omitempty"`
}

type callRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// heading names an event as search results and recall show it.
func (d convDoc) heading() string {
	h := fmt.Sprintf("seq %d, %s", d.Seq, d.What)
	if d.Agent != "" {
		h += ", in the " + d.Agent + "'s context"
	}
	return h
}

// convFold makes documents of a log's events, taken in order: those a
// model could have seen. Records of the guard, of requests and of notes
// are not the conversation, and neither is thinking. Searches and recalls
// are left out: their results repeat other events, and a search would
// find its own call. Its state between turns is kept in the .mem.
type convFold struct {
	// Agents are the open narrowings' agents, innermost last.
	Agents []string `json:"agents,omitempty"`
	// Calls are the calls made and not yet answered, which name their
	// results.
	Calls map[string]log.ToolCall `json:"calls,omitempty"`
}

func (c convFold) clone() convFold {
	return convFold{Agents: slices.Clone(c.Agents), Calls: maps.Clone(c.Calls)}
}

func (c *convFold) call(tc log.ToolCall) {
	if c.Calls == nil {
		c.Calls = map[string]log.ToolCall{}
	}
	c.Calls[tc.ID] = tc
}

// observe takes the next event and returns its document and the words to
// rank it by, if it is one.
func (c *convFold) observe(ev log.Event) (convDoc, []retrieval.Field, bool) {
	agent := ""
	if n := len(c.Agents); n > 0 {
		agent = c.Agents[n-1]
	}
	var d convDoc
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
			c.Agents = append(c.Agents, name)
		}
		return d, nil, false
	case log.KindContextFold:
		if n := len(c.Agents); n > 0 {
			c.Agents = c.Agents[:n-1]
		}
		return d, nil, false
	case log.KindContextWiden:
		c.Agents = nil
		return d, nil, false
	case log.KindToolCall:
		var p log.ToolCallEvent
		if ev.Decode(&p) == nil {
			c.call(p.ToolCall)
		}
		return d, nil, false
	case log.KindUserMsg:
		// A person's message ends every narrowing still open
		// (window.Build), and every call still unanswered.
		c.Agents, c.Calls, agent = nil, nil, ""
		d.What = "the person's message"
	case log.KindHarnessMsg:
		if agent != "" {
			// nodwyn's message in a helper's context is its prompt to the
			// helper (the planner's task, the explorer's), which repeats
			// the request and its own instructions: not the conversation.
			return d, nil, false
		}
		d.What = "nodwyn's message"
	case log.KindAttach:
		var p log.Attach
		if ev.Decode(&p) != nil {
			return d, nil, false
		}
		d.What = "the attached file " + p.Name
	case log.KindGatePlan:
		d.What = "the plan"
	case log.KindSummary:
		var p log.Summary
		if ev.Decode(&p) != nil {
			return d, nil, false
		}
		d.What = fmt.Sprintf("the summary of seq %d to %d", p.From, p.Through)
	case log.KindLLMResp:
		var p log.LLMResp
		if ev.Decode(&p) != nil {
			return d, nil, false
		}
		for _, tc := range p.ToolCalls {
			// The window pairs a result with its reply's call; a log may
			// have no tool.call record of it.
			c.call(tc)
			if !readsTheConversation(tc.Name) {
				d.Calls = append(d.Calls, callRef{tc.ID, tc.Name})
			}
		}
		d.What = "a reply"
	case log.KindToolResult:
		var p log.ToolResult
		if ev.Decode(&p) != nil {
			return d, nil, false
		}
		call, ok := c.Calls[p.ID]
		delete(c.Calls, p.ID)
		if readsTheConversation(p.Name) {
			return d, nil, false
		}
		if !ok {
			call = log.ToolCall{ID: p.ID, Name: p.Name}
		}
		d.What, d.Call = "the result of "+callLine(call), p.ID
		if !p.OK {
			d.What += ", which failed"
		}
		// What was asked names a result better than its output does.
		fields = []retrieval.Field{{Text: call.Name + " " + string(call.Args), Weight: 3}}
	default:
		return d, nil, false
	}
	// A reply that only searched or recalled, and a gate's verdict without
	// a plan, hold nothing to find.
	text := eventText(ev)
	if text == "" && (ev.Kind == log.KindLLMResp || ev.Kind == log.KindGatePlan) {
		return d, nil, false
	}
	d.Seq, d.Agent = ev.Seq, agent
	return d, append(fields, retrieval.Field{Text: text}), true
}

// heading names an event of l as search results and recall show it, a
// compaction's note as that.
func (l *logMemory) heading(d convDoc) string {
	h := d.heading()
	for _, e := range l.entries {
		if e.Level == memory.LevelEpisode && e.Note == d.Seq {
			h += fmt.Sprintf(", the note of a compaction of seq %d to %d", e.From, e.Through)
		}
	}
	return h
}

// turnOf is the turn that holds seq, by the seq of its first message, and
// that turn in a line: how it ended and what it asked. Events outside any
// turn (nodwyn's messages before the first) are 0.
func (l *logMemory) turnOf(seq uint64) (uint64, string) {
	for _, e := range l.entries {
		if e.Level == memory.LevelTurn && e.From <= seq && seq <= e.Through {
			return e.From, fmt.Sprintf("turn seq %d-%d · %s", e.From, e.Through, e.Head)
		}
	}
	if l.open != 0 && seq >= l.open {
		return l.open, fmt.Sprintf("turn seq %d-, not finished · asked %q", l.open, cut(l.asked, askedCut))
	}
	return 0, "outside a turn"
}

// find is the part and document of the event at seq, nil for none.
func (l *logMemory) find(seq uint64) (*wordsPart, int) {
	for _, p := range l.parts {
		if i := p.find(seq); i >= 0 {
			return p, i
		}
	}
	return nil, 0
}

// resultOf is the seq of the first result after seq that answers the call
// id, 0 when none does.
func (l *logMemory) resultOf(seq uint64, id string) uint64 {
	needle := []byte(`"call":`)
	for _, p := range l.parts {
		i, _ := slices.BinarySearch(p.seqs, seq+1)
		for ; i < len(p.seqs); i++ {
			if bytes.Contains(p.raw[i], needle) && p.doc(i).Call == id {
				return p.seqs[i]
			}
		}
	}
	return 0
}

// turnView is what recall adds to a turn's first message: the turn's card
// but for what it asked, which the message says, and the events it holds,
// so that one recall shows what came of a request.
func (l *logMemory) turnView(seq uint64) string {
	var b strings.Builder
	through := uint64(0)
	for _, e := range l.entries {
		if e.Level == memory.LevelTurn && e.From == seq {
			_, card, _ := strings.Cut(e.Text, "\n")
			fmt.Fprintf(&b, "\n\n[what the turn of seq %d to %d did]\n%s\n[the events it holds]", e.From, e.Through, card)
			through = e.Through
		}
	}
	if through == 0 && seq == l.open {
		fmt.Fprintf(&b, "\n\n[the turn from seq %d, not finished: the events it holds so far]", seq)
		through = l.last
	}
	if through == 0 {
		return ""
	}
	for _, p := range l.parts {
		i, _ := slices.BinarySearch(p.seqs, seq+1)
		for ; i < len(p.seqs) && p.seqs[i] <= through; i++ {
			b.WriteString("\n" + l.heading(p.doc(i)))
		}
	}
	return b.String()
}

// turnHits are the events a search found in one turn of one log.
type turnHits struct {
	log  int
	head string
	hits []hit
}

type hit struct {
	doc     convDoc
	matched []string
}

// rankTurns ranks the events of logs as one collection, the best limit of
// them grouped by the turn that holds each: the turns in the order of
// their best event, and each turn's events in the order they happened. It
// also says how many events it ranked.
func rankTurns(logs []*logMemory, query string, limit int) ([]turnHits, int) {
	var indexes []*retrieval.Index
	var parts []*wordsPart
	var owner []int
	events := 0
	for i, l := range logs {
		for _, p := range l.parts {
			indexes, parts, owner = append(indexes, p.index), append(parts, p), append(owner, i)
			events += p.index.Len()
		}
	}
	var groups []turnHits
	at := map[[2]uint64]int{}
	for _, f := range retrieval.SearchAll(indexes, query, limit, nil) {
		i := owner[f.Index]
		d := parts[f.Index].doc(f.Doc)
		from, head := logs[i].turnOf(d.Seq)
		key := [2]uint64{uint64(i), from}
		g, ok := at[key]
		if !ok {
			g, at[key] = len(groups), len(groups)
			groups = append(groups, turnHits{log: i, head: head})
		}
		groups[g].hits = append(groups[g].hits, hit{d, f.Matched})
	}
	for _, g := range groups {
		slices.SortFunc(g.hits, func(p, q hit) int { return cmp.Compare(p.doc.Seq, q.doc.Seq) })
	}
	return groups, events
}

// writeTurns writes groups as a search shows them: each turn in a line
// after its log's label, then each event found in it with its lines that
// hold the most of the words matched, numbered as recall numbers them.
// text reads an event's text again, from its log.
func writeTurns(b *strings.Builder, logs []*logMemory, groups []turnHits, label func(log int) string, text func(log int, seq uint64) (string, error)) {
	for _, g := range groups {
		b.WriteString(label(g.log) + g.head + "\n")
		for _, h := range g.hits {
			b.WriteString("  " + logs[g.log].heading(h.doc) + "\n")
			t, err := text(g.log, h.doc.Seq)
			if err != nil {
				continue // the log changed under the search; the heading says what it was
			}
			for _, l := range bestLines(t, h.matched) {
				fmt.Fprintf(b, "    %d: %s\n", l.n, cut(l.text, searchLineCut))
			}
		}
	}
}

// found is how many events groups hold.
func found(groups []turnHits) int {
	n := 0
	for _, g := range groups {
		n += len(g.hits)
	}
	return n
}

// recallIn returns the event of l at seq, read by read, under its heading
// after prefix: lines start to start+count-1 of it (count 0 is as many as
// fit), at most budget bytes, as a ranged fs-read returns a file's. A
// reply says where its calls' results are, which an elided reply's window
// no longer shows; a turn's first message says what the turn did and
// which events it holds.
func recallIn(l *logMemory, read func(uint64) (log.Event, error), seq uint64, start, count int64, budget int, prefix string) (string, error) {
	part, i := l.find(seq)
	switch {
	case part == nil && seq > l.last:
		return "", fmt.Errorf("there is no seq %d: the conversation's last is %d", seq, l.last)
	case part == nil:
		return "", fmt.Errorf("seq %d is not a message, a reply or a tool result: recall brings back those, by the N of [elided seq=N] or a seq index-search gave", seq)
	}
	d := part.doc(i)
	ev, err := read(seq)
	if err != nil {
		return "", err
	}
	text := eventText(ev)
	for _, c := range d.Calls {
		if r := l.resultOf(seq, c.ID); r != 0 {
			text += fmt.Sprintf("\n[the result of %s is seq %d]", c.Name, r)
		} else {
			text += fmt.Sprintf("\n[%s has no result recorded]", c.Name)
		}
	}
	text += l.turnView(seq)
	head := "[" + prefix + l.heading(d) + "]\n"
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

// ownMemory is this session's memory, with the postings of the words keep
// allows, and a reader of its events: from its .mem and its log on disk,
// or, for a log that is not a file, from the events it holds, all of it.
func (s *Session) ownMemory(keep func(string) bool) (*logMemory, func(uint64) (log.Event, error), error) {
	if s.opts.LogWriter != nil {
		events := s.EventsSoFar()
		l, err := foldEvents(s.id, events)
		read := func(seq uint64) (log.Event, error) {
			i, ok := slices.BinarySearchFunc(events, seq, func(ev log.Event, seq uint64) int { return cmp.Compare(ev.Seq, seq) })
			if !ok {
				return log.Event{}, fmt.Errorf("there is no seq %d", seq)
			}
			return events[i], nil
		}
		return l, read, err
	}
	store := Store{Dir: s.opts.Paths.SessionsDir}
	l, err := store.memory(s.id, keep)
	return l, func(seq uint64) (log.Event, error) { return store.eventAt(s.id, seq) }, err
}

// searchConversation is index-search's "conversation" and "earlier"
// scopes (earlier.go).
func (s *Session) searchConversation(_ context.Context, scope, query string, limit int) (string, error) {
	if scope == "earlier" {
		return s.searchEarlier(query, limit)
	}
	l, read, err := s.ownMemory(queryWords(query))
	if err != nil {
		return "", err
	}
	logs := []*logMemory{l}
	groups, events := rankTurns(logs, query, limit)
	if len(groups) == 0 {
		return fmt.Sprintf("No event of this conversation holds any of these words (%d events searched).", events), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[the %d best of %d events, best first, under the turns that hold them; recall brings an event back whole by its seq, and a turn's first message with what the turn did]\n", found(groups), events)
	writeTurns(&b, logs, groups, func(int) string { return "" }, func(_ int, seq uint64) (string, error) {
		ev, err := read(seq)
		return eventText(ev), err
	})
	return b.String(), nil
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
				Doc: "Bring back one event of this task's conversation as it was recorded: a message, a reply with its tool calls, or a tool result, by its seq (the N of [elided seq=N], or a seq index-search gave); with task, of an earlier task of this project that index-search with scope \"earlier\" found. It comes back whole, a long one in lines as a ranged fs-read reads a file, so a result that was elided, folded or cut can be read again without running its command. A turn's first message comes back with what the turn did (how it ended, its plan, files written, commands run, errors, its answer) and the seq of each event in it. It shows what was then, not what is now: read a file again with fs-read.",
				Params: []scheme.Param{
					{Name: "seq", Type: "integer", Doc: "the event's seq"},
					{Name: "start", Type: "integer", Doc: "the 1-based line to start at; 1 unless given", Optional: true},
					{Name: "count", Type: "integer", Doc: "how many lines to return; as many as fit unless given", Optional: true},
					{Name: "task", Type: "string", Doc: "an earlier task's id, as index-search with scope \"earlier\" names it; this task unless given", Optional: true},
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
				task := ""
				if len(c.Args) > 3 {
					if task, err = c.String(3); err != nil {
						return scheme.Void, err
					}
				}
				var out string
				if task != "" && task != s.id {
					out, err = s.recallEarlier(task, uint64(seq), start, count)
				} else {
					var l *logMemory
					var read func(uint64) (log.Event, error)
					if l, read, err = s.ownMemory(noWords); err == nil {
						out, err = recallIn(l, read, uint64(seq), start, count, s.rangeBytes(), "")
					}
				}
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

func readsTheConversation(tool string) bool { return tool == "index-search" || tool == "recall" }

// eventText is an event's text as index-search ranks it and recall returns
// it, "" for a kind they do not read. It depends on the event alone, so an
// earlier task's event read again by its seq reads as it was indexed.
func eventText(ev log.Event) string {
	switch ev.Kind {
	case log.KindUserMsg:
		var p log.UserMsg
		if ev.Decode(&p) == nil {
			return p.Text
		}
	case log.KindHarnessMsg:
		var p log.HarnessMsg
		if ev.Decode(&p) == nil {
			return p.Text
		}
	case log.KindAttach:
		var p log.Attach
		if ev.Decode(&p) == nil {
			return window.AttachmentText(p)
		}
	case log.KindGatePlan:
		var p log.GatePlan
		if ev.Decode(&p) == nil {
			return p.Plan
		}
	case log.KindSummary:
		var p log.Summary
		if ev.Decode(&p) == nil {
			return p.Text
		}
	case log.KindLLMResp:
		var p log.LLMResp
		if ev.Decode(&p) != nil {
			return ""
		}
		var lines []string
		if p.Content != "" {
			lines = append(lines, p.Content)
		}
		for _, tc := range p.ToolCalls {
			if !readsTheConversation(tc.Name) {
				lines = append(lines, tc.Name+" "+string(tc.Args))
			}
		}
		return strings.Join(lines, "\n")
	case log.KindToolResult:
		var p log.ToolResult
		if ev.Decode(&p) != nil || readsTheConversation(p.Name) {
			return ""
		}
		if !p.OK {
			return p.Error
		}
		return resultText(p.Value)
	}
	return ""
}

// resultText is a tool result as text with lines, so recall can page it.
// A result is recorded as its tool returned it, and a shell's or a
// search's is one line of JSON. A shell result reads as the model read it
// (shell.scm's shell-plain: the exit status, the output, then [stderr]
// and the errors), but whole; other JSON is indented; anything else is as
// recorded.
func resultText(value string) string {
	var shell struct {
		Status any     `json:"status"` // an exit code, or why it did not end ("timed out after 10m0s")
		Stdout *string `json:"stdout"`
		Stderr *string `json:"stderr"`
	}
	d := json.NewDecoder(strings.NewReader(value))
	d.UseNumber()
	if d.Decode(&shell) == nil && shell.Status != nil && (shell.Stdout != nil || shell.Stderr != nil) {
		text := fmt.Sprint(shell.Status)
		if _, ended := shell.Status.(json.Number); ended {
			text = "exit " + text
		}
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
