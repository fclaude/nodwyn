package window

import "github.com/fclaude/nodwyn/internal/log"

// StartTracker follows, event by event, where a window built from the events
// so far begins: the narrowing at which a request last cut the conversation.
// buildV1 cuts at each user message, at the innermost lasting narrowing
// still open, and nothing before the cut reaches a later window: entries
// before it are dropped, the task and plan start again at the message, the
// narrowings open then are closed, and elisions and folds are named by seq.
// So a window built from the events from Start on is the window built from
// every event (TestWindowFromItsStart), and a resumed session need hold only
// those (ADR 49 §10). Its fields are exported for the log's index, which
// keeps it as JSON.
type StartTracker struct {
	Frames []StartFrame `json:"frames,omitempty"`
	Cut    uint64       `json:"cut,omitempty"`
}

// StartFrame is a narrowing still open: where it was recorded, and whether
// it lasts past its turn.
type StartFrame struct {
	Seq     uint64 `json:"seq"`
	Lasting bool   `json:"lasting,omitempty"`
}

// Observe takes the next event, as buildV1's second pass takes it.
func (t *StartTracker) Observe(ev log.Event) {
	switch ev.Kind {
	case log.KindUserMsg:
		var p log.UserMsg
		if ev.Decode(&p) != nil {
			return
		}
		for i := len(t.Frames) - 1; i >= 0; i-- {
			if t.Frames[i].Lasting {
				t.Cut = t.Frames[i].Seq
				break
			}
		}
		t.Frames = nil
	case log.KindContextNarrow:
		var p log.ContextNarrow
		if ev.Decode(&p) == nil {
			t.Frames = append(t.Frames, StartFrame{Seq: ev.Seq, Lasting: p.Lasting})
		}
	case log.KindContextWiden:
		t.Frames = nil
	case log.KindContextFold:
		if n := len(t.Frames); n > 0 {
			t.Frames = t.Frames[:n-1]
		}
	}
}

// Start is the seq of the first event a window built now reads: 1 before
// any cut.
func (t *StartTracker) Start() uint64 { return max(t.Cut, 1) }
