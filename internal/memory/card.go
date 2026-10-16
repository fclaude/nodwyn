// Package memory is what a task remembers of its history (ADR 51). Entries
// are made from a session log's events: a card for each turn, made here in
// Go and costing nothing, and an episode for each note the model wrote when
// it compacted the conversation. They are a derived view of the log, never
// recorded in it: whatever reads the log can make them again. index-search
// shows the events it finds under their turns' cards, and recall of a
// turn's first message shows its card (ADR 50, amended).
package memory

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fclaude/nodwyn/internal/log"
)

// Levels of memory: a turn's card, and an episode the model summarized.
const (
	LevelTurn    = 1
	LevelEpisode = 2
)

// Entry is a turn's card or an episode. Its ID names the session, the span
// of the log it comes from and its level, so making it again from the same
// log gives the same ID, which replaces it.
type Entry struct {
	ID      string    `json:"id"`
	Level   int       `json:"level"`
	Session string    `json:"session"`
	From    uint64    `json:"from"`
	Through uint64    `json:"through"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	// Head is a card in one line: how the turn ended and what was asked.
	Head string `json:"head,omitempty"`
	// Note is an episode's note, the seq of the reply that holds it.
	Note uint64 `json:"note,omitempty"`
	Text string `json:"text"`
}

// EntryID is the ID of the entry of level for a session's span.
func EntryID(session string, from, through uint64, level int) string {
	return fmt.Sprintf("m:%s:%d-%d:%d", session, from, through, level)
}

// What a card keeps of each part of a turn.
const (
	maxAsked     = 1500
	maxAnswer    = 1200
	maxNote      = 6000
	maxFiles     = 12
	maxRan       = 8
	maxErrors    = 5
	maxLineLen   = 160
	maxHeadAsked = 160
	noteReplies  = 8 // the replies kept to find a compaction's note
)

// State is what a Folder carries from one turn to the next: where, and
// when, the conversation the next compaction note covers begins. Between
// turns it is all there is, so an index of a log keeps it with the point it
// folded to.
type State struct {
	EpisodeFrom  uint64    `json:"episode_from,omitempty"`
	EpisodeStart time.Time `json:"episode_start,omitzero"`
}

// Folder makes entries from a session log's events, taken in order.
type Folder struct {
	Session string
	State
	turn    *turn
	replies []reply
}

type reply struct {
	seq  uint64
	text string
}

type turn struct {
	from    uint64
	start   time.Time
	last    time.Time
	asked   string
	plan    string
	marks   map[int]log.PlanStep
	wrote   []string
	calls   map[string]string // a command's call ID → its command line
	ran     []string
	errors  []string
	answer  string
	cost    float64
	unknown bool // a reply's cost was not known
}

// Open is the turn begun and not ended: the seq of the message that began
// it and what it asked, 0 when there is none. A point after an event where
// none is open is one a log's memory may fold to.
func (f *Folder) Open() (from uint64, asked string) {
	if f.turn == nil {
		return 0, ""
	}
	return f.turn.from, oneLine(f.turn.asked)
}

// Observe takes the next event and returns the entries it completes.
func (f *Folder) Observe(ev log.Event) []Entry {
	var out []Entry
	if f.EpisodeFrom == 0 {
		f.EpisodeFrom, f.EpisodeStart = ev.Seq, ev.TS
	}
	switch ev.Kind {
	case log.KindUserMsg:
		var m log.UserMsg
		if ev.Decode(&m) != nil {
			return nil
		}
		if f.turn != nil {
			// A turn nothing ended: the process stopped, or an older log.
			out = append(out, f.close(ev.Seq-1, f.turn.last, "unfinished", ""))
		}
		f.turn = &turn{from: ev.Seq, start: ev.TS, last: ev.TS, asked: clip(m.Text, maxAsked), marks: map[int]log.PlanStep{}, calls: map[string]string{}}
	case log.KindSessionResume, log.KindSessionEnd:
		if f.turn != nil {
			out = append(out, f.close(ev.Seq-1, f.turn.last, "interrupted", ""))
		}
	case log.KindTurnEnd:
		var p log.TurnEnd
		if f.turn != nil && ev.Decode(&p) == nil {
			outcome := p.Outcome
			if outcome == "" {
				outcome = "completed"
			}
			out = append(out, f.close(ev.Seq, ev.TS, outcome, p.Error))
		}
	case log.KindLLMResp:
		var p log.LLMResp
		if ev.Decode(&p) != nil {
			return nil
		}
		f.replies = append(f.replies, reply{ev.Seq, p.Content})
		if len(f.replies) > noteReplies {
			f.replies = f.replies[1:]
		}
		if t := f.turn; t != nil {
			if len(p.ToolCalls) == 0 && strings.TrimSpace(p.Content) != "" {
				t.answer = clip(p.Content, maxAnswer)
			}
			if p.Usage.Cost != nil && *p.Usage.Cost > 0 {
				t.cost += *p.Usage.Cost
			} else {
				t.unknown = true
			}
		}
	case log.KindContextCompact:
		var p log.ContextCompact
		if ev.Decode(&p) != nil || !p.Compacted {
			return out
		}
		// The note is the reply record-compaction names, or in a log that
		// names none the latest reply, which is the one it would name.
		noteSeq := p.NoteSeq
		if noteSeq == 0 && len(f.replies) > 0 {
			noteSeq = f.replies[len(f.replies)-1].seq
		}
		for _, r := range f.replies {
			if r.seq == noteSeq && strings.TrimSpace(r.text) != "" {
				from := min(f.EpisodeFrom, ev.Seq)
				text := fmt.Sprintf("compaction note (%s), written when the conversation reached %d tokens:\n%s", p.Trigger, p.Before, clip(r.text, maxNote))
				out = append(out, Entry{ID: EntryID(f.Session, from, ev.Seq, LevelEpisode), Level: LevelEpisode, Session: f.Session, From: from, Through: ev.Seq, Start: f.EpisodeStart, End: ev.TS, Note: noteSeq, Text: text})
				break
			}
		}
		// The next episode begins after this one; its first event says when.
		f.EpisodeFrom, f.EpisodeStart = ev.Seq+1, time.Time{}
	}
	if f.EpisodeStart.IsZero() && ev.Seq >= f.EpisodeFrom {
		f.EpisodeStart = ev.TS
	}
	if t := f.turn; t != nil {
		t.last = ev.TS
		t.observe(ev)
	}
	return out
}

var exitStatus = regexp.MustCompile(`"status"\D{0,8}(-?\d+)`)

// observe keeps what the turn's card says of an event.
func (t *turn) observe(ev log.Event) {
	switch ev.Kind {
	case log.KindGatePlan:
		var p log.GatePlan
		if ev.Decode(&p) == nil && strings.TrimSpace(p.Plan) != "" {
			t.plan, t.marks = p.Plan, map[int]log.PlanStep{}
			for _, m := range p.Marks {
				t.marks[m.Step] = m
			}
		}
	case log.KindPlanStep:
		var p log.PlanStep
		if t.plan != "" && ev.Decode(&p) == nil {
			t.marks[p.Step] = p
		}
	case log.KindGuardDecision:
		var d log.GuardDecision
		if ev.Decode(&d) == nil && d.Wrote() {
			for _, w := range t.wrote {
				if w == d.Path {
					return
				}
			}
			t.wrote = append(t.wrote, d.Path)
		}
	case log.KindToolCall:
		var c log.ToolCallEvent
		if ev.Decode(&c) != nil || (c.Name != "shell-exec" && c.Name != "shell-start") {
			return
		}
		var a struct {
			Cmd  string   `json:"cmd"`
			Args []string `json:"args"`
		}
		if json.Unmarshal(c.Args, &a) == nil && a.Cmd != "" {
			t.calls[c.ID] = clip(strings.TrimSpace(a.Cmd+" "+strings.Join(a.Args, " ")), 120)
		}
	case log.KindToolResult:
		var r log.ToolResult
		if ev.Decode(&r) != nil {
			return
		}
		if cmd, ok := t.calls[r.ID]; ok {
			delete(t.calls, r.ID)
			switch m := exitStatus.FindStringSubmatch(r.Value); {
			case !r.OK:
				cmd += " (failed: " + firstLine(r.Error) + ")"
			case m != nil:
				cmd += " (exit " + m[1] + ")"
			}
			t.ran = append(t.ran, cmd)
		} else if !r.OK && r.Error != "" {
			t.errors = append(t.errors, r.Name+": "+firstLine(r.Error))
		}
	}
}

// close makes the turn's card, the turn ending at through.
func (f *Folder) close(through uint64, end time.Time, outcome, why string) Entry {
	t := f.turn
	f.turn = nil
	ended := outcome
	if why != "" {
		ended += ": " + firstLine(why)
	}
	ended += " · " + end.Sub(t.start).Round(time.Second).String()
	if t.cost > 0 {
		at := ""
		if t.unknown {
			at = "at least "
		}
		ended += fmt.Sprintf(" · %s$%.2f", at, t.cost)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "asked   %s\noutcome %s\n", oneLine(t.asked), ended)
	if t.plan != "" {
		var steps []string
		for i, step := range (log.GatePlan{Plan: t.plan}).Steps() {
			mark := "·"
			if m, ok := t.marks[i+1]; ok {
				mark = map[string]string{"done": "✓", "skipped": "✗"}[m.State]
			}
			steps = append(steps, fmt.Sprintf("%d %s %s", i+1, mark, clip(oneLine(step), 80)))
		}
		fmt.Fprintf(&b, "plan    %s\n", strings.Join(steps, "  "))
	}
	if len(t.wrote) > 0 {
		fmt.Fprintf(&b, "wrote   %s\n", list(t.wrote, maxFiles, ", "))
	}
	if len(t.ran) > 0 {
		fmt.Fprintf(&b, "ran     %s\n", list(t.ran, maxRan, " · "))
	}
	if len(t.errors) > 0 {
		fmt.Fprintf(&b, "errors  %s\n", list(t.errors, maxErrors, " · "))
	}
	if t.answer != "" {
		fmt.Fprintf(&b, "answer  %s\n", oneLine(t.answer))
	}
	head := fmt.Sprintf("%s · asked %q", ended, clip(oneLine(t.asked), maxHeadAsked))
	return Entry{ID: EntryID(f.Session, t.from, through, LevelTurn), Level: LevelTurn, Session: f.Session, From: t.from, Through: through, Start: t.start, End: end, Head: head, Text: strings.TrimRight(b.String(), "\n")}
}

// list joins items, the first max of them and how many more.
func list(items []string, max int, sep string) string {
	if len(items) <= max {
		return strings.Join(items, sep)
	}
	return strings.Join(items[:max], sep) + fmt.Sprintf("%sand %d more", sep, len(items)-max)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return clip(line, maxLineLen)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// clip cuts s to at most n bytes at a character boundary, and says so.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
