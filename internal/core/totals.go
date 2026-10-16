package core

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
)

// SessionMark identifies a durable boundary without copying its conversation.
type SessionMark struct {
	Seq uint64    `json:"seq"`
	TS  time.Time `json:"ts"`
}

type indexMark struct {
	SessionMark
	Offset   int64  `json:"offset"`
	Reply    string `json:"reply,omitempty"`
	ReplySeq uint64 `json:"reply_seq,omitempty"`
	// Context is the setup a run inherits (its session.start or resume,
	// model, judge, helpers and limits), so the latest run opens without a
	// read of the log before it. Only the latest run keeps the events whole:
	// they are tens of kilobytes each, the system prompt and tool schemas
	// among them, and kept in every run's mark they grew the index by that
	// much a run. An earlier run's keep their seq and kind, and are read
	// from the log when that run is opened.
	Context []log.Event `json:"context,omitempty"`
}

// SessionTotals covers a complete durable prefix, including records no longer
// held by a resumed session. Open clocks are timestamps in milliseconds, not
// claims that a process still runs; clients extend them only while it is busy.
type SessionTotals struct {
	Through     uint64              `json:"through"`
	Truncated   bool                `json:"truncated"`
	Started     bool                `json:"started"`
	Cost        float64             `json:"cost"`
	Unpriced    int                 `json:"unpriced"`
	Estimated   bool                `json:"estimated"`
	Cache       CacheTotals         `json:"cache"`
	Speed       SpeedTotals         `json:"speed"`
	Judge       JudgeTotals         `json:"judge"`
	Clock       ClockTotals         `json:"clock"`
	Changes     []log.GuardDecision `json:"changes"`
	Attachments []log.Attach        `json:"attachments"`
	Turns       int                 `json:"turns"`
	Runs        int                 `json:"runs"`
	LastTurn    *SessionMark        `json:"last_turn,omitempty"`
	LastRun     *SessionMark        `json:"last_run,omitempty"`
}

type CacheTotals struct {
	Prompt int64 `json:"prompt"`
	Cached int64 `json:"cached"`
}

type SpeedTotals struct {
	Out     int64 `json:"out"`
	MS      int64 `json:"ms"`
	Replies int   `json:"replies"`
}

type JudgeTotals struct {
	Calls    int     `json:"calls"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
	Unpriced int     `json:"unpriced"`
}

type ClockTotals struct {
	Total     int64 `json:"total"`
	Work      int64 `json:"work"`
	Model     int64 `json:"model"`
	Tools     int64 `json:"tools"`
	Open      int64 `json:"open"`
	Prev      int64 `json:"prev"`
	OpenModel int64 `json:"open_model"`
	OpenTool  int64 `json:"open_tool"`
}

// totalsFold keeps running sums and only the unfinished pairs needed to fold
// another event. Tool spans wait for the outermost call to end so nested calls
// and their approval waits are counted once, whichever result arrives first.
type totalsFold struct {
	SessionTotals
	Gap        int64            `json:"gap"`
	GapWaited  bool             `json:"gap_waited"`
	GapResumed bool             `json:"gap_resumed"`
	RunTurn    bool             `json:"run_turn"`
	SpeedOpen  int64            `json:"speed_open"`
	Calls      map[string]int64 `json:"calls,omitempty"`
	ToolSpans  []timeSpan       `json:"tool_spans,omitempty"`
	Waits      []timeSpan       `json:"waits,omitempty"`
}

type timeSpan struct{ From, To int64 }

// Totals is a read of the index, brought forward to the log's last complete
// event. Legacy approval previews are redacted just as log.read redacts them.
func (s Store) Totals(id string) (SessionTotals, error) {
	idx, truncated, err := s.index(id)
	if err != nil {
		return SessionTotals{}, err
	}
	t := idx.Totals.SessionTotals
	t.Through, t.Truncated = idx.Seq, truncated
	t.Turns, t.Runs = len(idx.Turns), len(idx.Runs)
	if t.Turns > 0 {
		m := idx.Turns[t.Turns-1].SessionMark
		t.LastTurn = &m
	}
	if t.Runs > 0 {
		m := idx.Runs[t.Runs-1].SessionMark
		t.LastRun = &m
	}
	for _, ts := range idx.Totals.Calls {
		if t.Clock.OpenTool == 0 || ts < t.Clock.OpenTool {
			t.Clock.OpenTool = ts
		}
	}
	spans := idx.Totals.ToolSpans
	if t.Clock.OpenTool > 0 {
		spans = append(slices.Clone(spans), timeSpan{t.Clock.OpenTool, max(t.Clock.OpenTool, t.Clock.Prev)})
		// The completed portion already includes approval waits; only time after
		// the prefix's end may be extended by a live client.
		t.Clock.OpenTool = max(t.Clock.OpenTool, t.Clock.Prev)
	}
	t.Clock.Tools += toolTime(spans, idx.Totals.Waits)
	// Redacting the derived view does not change what the index records.
	raw, err := json.Marshal(t)
	if err != nil {
		return SessionTotals{}, err
	}
	raw, _, err = redact.JSON(raw, nil)
	if err != nil {
		return SessionTotals{}, err
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return SessionTotals{}, err
	}
	return t, nil
}

func (idx *logIndex) observeTotals(ev log.Event, offset int64) {
	t := &idx.Totals
	mark := indexMark{SessionMark: SessionMark{Seq: ev.Seq, TS: ev.TS}, Offset: offset}
	switch ev.Kind {
	case log.KindScheduleRun:
		idx.Runs = append(idx.Runs, mark)
	case log.KindRunState:
		var p log.RunState
		if ev.Decode(&p) == nil && p.Status == "running" && p.Turns == 0 {
			idx.Runs = append(idx.Runs, mark)
		}
	case log.KindRunTurn:
		idx.Turns = append(idx.Turns, mark)
		t.RunTurn = true
	case log.KindUserMsg, log.KindEvalReq:
		if !t.RunTurn {
			idx.Turns = append(idx.Turns, mark)
		}
		t.RunTurn = false
	case log.KindTurnEnd, log.KindSessionResume, log.KindSessionEnd:
		t.RunTurn = false
	}
	t.observe(ev)
}

func (t *totalsFold) observe(ev log.Event) {
	ts := int64(0)
	if !ev.TS.IsZero() {
		ts = ev.TS.UnixMilli()
	}
	c := &t.Clock
	waited := waitedOn(ev)
	cut := ev.Kind == log.KindSessionResume || ev.Kind == log.KindSessionEnd
	if c.Prev > 0 && ts > c.Prev && c.Open > 0 && !cut {
		span := ts - c.Prev
		c.Total += span
		if !waited {
			c.Work += span
		}
	}
	if cut {
		c.Open, t.Gap = 0, 0
	}
	if c.Open == 0 && t.Gap > 0 {
		t.GapWaited = t.GapWaited || waited
		t.GapResumed = t.GapResumed || ev.Kind == log.KindRunState || ev.Kind == log.KindSessionResume
		if ev.Kind == log.KindRunCheckpoint {
			var p log.RunCheckpoint
			if ev.Decode(&p) == nil && !p.Allowed {
				t.Gap = 0
			}
		}
	}
	switch ev.Kind {
	case log.KindTurnEnd:
		c.Open, t.Gap, t.GapWaited, t.GapResumed = 0, ts, false, false
	case log.KindUserMsg, log.KindEvalReq, log.KindRunTurn:
		t.Started = true
		if c.Open == 0 && t.Gap > 0 && ev.Kind == log.KindRunTurn && !t.GapWaited && !t.GapResumed {
			span := max(int64(0), ts-t.Gap)
			c.Total += span
			c.Work += span
		}
		t.Gap, c.Open = 0, ts
	}
	switch ev.Kind {
	case log.KindLLMReq:
		c.OpenModel, t.SpeedOpen = ts, ts
	case log.KindLLMResp:
		if c.OpenModel > 0 && ts > c.OpenModel {
			c.Model += ts - c.OpenModel
		}
		c.OpenModel = 0
		var p log.LLMResp
		if ev.Decode(&p) == nil {
			u := p.Usage
			if u.Cost != nil {
				t.Cost += *u.Cost
				t.Estimated = t.Estimated || u.CostEstimated
			} else {
				t.Unpriced++
			}
			if !u.Unreported && u.CachedTokens != nil && u.PromptTokens > 0 {
				t.Cache.Prompt += int64(u.PromptTokens)
				t.Cache.Cached += int64(*u.CachedTokens)
			}
			wall := u.WallMS
			if wall == 0 && t.SpeedOpen > 0 && ts > t.SpeedOpen {
				wall = ts - t.SpeedOpen
			}
			if wall > 0 && !u.Unreported && u.OutputTokens > 0 {
				t.Speed.Out += int64(u.OutputTokens)
				t.Speed.MS += wall
				t.Speed.Replies++
			}
		}
		t.SpeedOpen = 0
	case log.KindAttach:
		var p log.Attach
		if ev.Decode(&p) == nil {
			p.Text = ""
			t.Attachments = append(t.Attachments, p)
		}
	case log.KindJevReq:
		var p log.JevReq
		if ev.Decode(&p) == nil {
			t.Judge.Calls++
			if u := p.Usage; u != nil {
				t.Judge.Tokens += int64(u.InputTokens + u.OutputTokens)
				if u.Cost == nil {
					t.Judge.Unpriced++
					t.Unpriced++
				} else {
					t.Judge.Cost += *u.Cost
					t.Cost += *u.Cost
					t.Estimated = t.Estimated || *u.Cost > 0
				}
			}
		}
	case log.KindToolCall:
		var p log.ToolCallEvent
		if ev.Decode(&p) == nil && ts > 0 {
			if t.Calls == nil {
				t.Calls = map[string]int64{}
			}
			t.Calls[p.ID] = ts
		}
	case log.KindToolResult:
		var p log.ToolResult
		if ev.Decode(&p) == nil {
			if start := t.Calls[p.ID]; start > 0 && ts > start {
				t.ToolSpans = append(t.ToolSpans, timeSpan{start, ts})
			}
			delete(t.Calls, p.ID)
		}
	}
	if waited && c.Prev > 0 && ts > c.Prev && len(t.Calls) > 0 {
		t.Waits = append(t.Waits, timeSpan{c.Prev, ts})
	}
	if cut || ev.Kind == log.KindTurnEnd {
		c.OpenModel = 0
		clear(t.Calls)
	}
	if len(t.Calls) == 0 {
		c.Tools += toolTime(t.ToolSpans, t.Waits)
		t.ToolSpans, t.Waits = nil, nil
	}
	if ev.Kind == log.KindGuardDecision {
		var p log.GuardDecision
		if ev.Decode(&p) == nil && p.Wrote() {
			t.Changes = slices.DeleteFunc(t.Changes, func(old log.GuardDecision) bool { return old.Path == p.Path })
			t.Changes = append(t.Changes, p)
		}
	}
	c.Prev = ts
}

// waitedOn follows the console's receipt rule: an answer's preceding interval
// was a person's wait unless the judge or an unattended run answered it.
func waitedOn(ev log.Event) bool {
	var p struct {
		Actor, Decision string
		PromptID        string `json:"prompt_id"`
	}
	if ev.Decode(&p) != nil || p.Actor == "judge" || p.Actor == "unattended" {
		return false
	}
	if ev.Kind == log.KindGuardDecision {
		return p.Decision == "confirm"
	}
	return ev.Kind == log.KindTurnCheckpoint || p.PromptID != ""
}

func mergeTime(spans []timeSpan) []timeSpan {
	spans = slices.Clone(spans)
	slices.SortFunc(spans, func(a, b timeSpan) int { return cmp.Compare(a.From, b.From) })
	out := []timeSpan{}
	for _, s := range spans {
		if n := len(out); n > 0 && s.From <= out[n-1].To {
			out[n-1].To = max(out[n-1].To, s.To)
		} else {
			out = append(out, s)
		}
	}
	return out
}

func toolTime(spans, waits []timeSpan) int64 {
	var total int64
	for _, s := range mergeTime(spans) {
		total += s.To - s.From
		for _, w := range mergeTime(waits) {
			total -= max(int64(0), min(s.To, w.To)-max(s.From, w.From))
		}
	}
	return total
}
