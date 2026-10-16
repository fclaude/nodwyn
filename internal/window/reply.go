package window

import "github.com/fclaude/nodwyn/internal/log"

// LastReply returns the model's last reply of the current turn, the one
// since the latest user message, that a window built now would still show.
// A reply inside a folded context, such as a sub-agent's, does not count:
// the conversation no longer contains it. Narrowing, folding and widening
// follow the same rules as Build. ok is false when the turn has no reply
// left in view.
func LastReply(events []log.Event) (reply log.LLMResp, ok bool) {
	events = WithoutHandovers(events)
	var replies []log.LLMResp
	var frames []int // where each open narrowed context starts in replies
	for _, ev := range events {
		switch ev.Kind {
		case log.KindUserMsg:
			replies, frames = nil, nil
		case log.KindContextNarrow:
			frames = append(frames, len(replies))
		case log.KindContextWiden:
			frames = nil
		case log.KindContextFold:
			if n := len(frames); n > 0 {
				replies, frames = replies[:frames[n-1]], frames[:n-1]
			}
		case log.KindLLMResp:
			var p log.LLMResp
			if ev.Decode(&p) == nil {
				replies = append(replies, p)
			}
		}
	}
	if len(replies) == 0 {
		return log.LLMResp{}, false
	}
	return replies[len(replies)-1], true
}

// WithoutHandovers is events without the exchanges that wrote a scheduled
// task's handover notes: the ask for the note and the note, between a
// context.handover's From and Through. The note is for the next scheduled
// run, which starts from it; the conversation goes on as if it was never
// asked, and its last answer is the agent's, not the note.
func WithoutHandovers(events []log.Event) []log.Event {
	type span struct{ from, through uint64 }
	var spans []span
	for _, ev := range events {
		if ev.Kind != log.KindContextHandover {
			continue
		}
		var p log.ContextHandover
		if ev.Decode(&p) == nil && p.From > 0 && p.From <= p.Through {
			spans = append(spans, span{p.From, p.Through})
		}
	}
	if len(spans) == 0 {
		return events
	}
	out := make([]log.Event, 0, len(events))
	for _, ev := range events {
		in := false
		for _, s := range spans {
			if ev.Seq >= s.from && ev.Seq <= s.through {
				in = true
				break
			}
		}
		if !in {
			out = append(out, ev)
		}
	}
	return out
}
