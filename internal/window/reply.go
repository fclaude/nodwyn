package window

import "github.com/fclaude/nibb/internal/log"

// LastReply returns the model's last reply of the current turn, the one
// since the latest user message, that a window built now would still show.
// A reply inside a folded context, such as a sub-agent's, does not count:
// the conversation no longer contains it. Narrowing, folding and widening
// follow the same rules as Build. ok is false when the turn has no reply
// left in view.
func LastReply(events []log.Event) (reply log.LLMResp, ok bool) {
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
