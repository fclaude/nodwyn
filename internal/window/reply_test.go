package window

import (
	"testing"

	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

// LastReply agrees with Build: it is the last assistant message the next
// request would carry.
func TestLastReplyIsWhatTheWindowShows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps func(s *session)
		want  string
	}{
		{"plain", func(s *session) {
			s.w.Append(log.KindLLMResp, log.LLMResp{Content: "answer"})
		}, "answer"},
		{"folded sub-agent", func(s *session) {
			s.w.Append(log.KindLLMResp, log.LLMResp{Content: "answer\nNODWYN_RUN_COMPLETE"})
			s.w.Append(log.KindContextNarrow, log.ContextNarrow{System: "You review."})
			s.w.Append(log.KindHarnessMsg, log.HarnessMsg{Text: "Review it."})
			s.w.Append(log.KindLLMResp, log.LLMResp{Content: "VERDICT: done"})
			s.w.Append(log.KindContextFold, log.ContextFold{})
		}, "answer\nNODWYN_RUN_COMPLETE"},
		{"open narrowed context", func(s *session) {
			s.w.Append(log.KindLLMResp, log.LLMResp{Content: "before"})
			s.w.Append(log.KindContextNarrow, log.ContextNarrow{System: "Continue from the note."})
			s.w.Append(log.KindHarnessMsg, log.HarnessMsg{Text: "note"})
			s.w.Append(log.KindLLMResp, log.LLMResp{Content: "finished after compaction"})
		}, "finished after compaction"},
		{"widened", func(s *session) {
			s.w.Append(log.KindContextNarrow, log.ContextNarrow{System: "Implement."})
			s.w.Append(log.KindHarnessMsg, log.HarnessMsg{Text: "plan"})
			s.w.Append(log.KindLLMResp, log.LLMResp{Content: "implemented"})
			s.w.Append(log.KindContextWiden, log.ContextWiden{})
		}, "implemented"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSession()
			s.turn(10)
			s.w.Append(log.KindUserMsg, log.UserMsg{Text: "do it"})
			tc.steps(s)
			evs := s.events(t)
			got, ok := LastReply(evs)
			if !ok || got.Content != tc.want {
				t.Fatalf("LastReply = %q, %v; want %q", got.Content, ok, tc.want)
			}
			msgs := Build(evs, "sys", 0, pol).Messages
			if last := msgs[len(msgs)-1]; last.Role != llm.RoleAssistant || last.Content != tc.want {
				t.Fatalf("Build's last message is %+v; LastReply disagrees", last)
			}
		})
	}
	// A turn with no reply yet has none: an earlier turn's does not count.
	s := newSession()
	s.turn(10)
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "new"})
	if r, ok := LastReply(s.events(t)); ok {
		t.Fatalf("reply from an earlier turn: %+v", r)
	}
}
