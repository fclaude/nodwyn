package window

import (
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
)

// A hidden plan (the lean profile's one-line plan) is recorded and not shown
// to the model, in the system prompt or in the conversation, and it takes the
// place of a plan shown before it (ADR 32, amended 2026-10-17).
func TestAHiddenPlanIsNotShown(t *testing.T) {
	s := newSession()
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "run turn 1"})
	s.w.Append(log.KindGatePlan, log.GatePlan{Noul: "required", Plan: "1. make the change", Hidden: true})
	for _, p := range []Policy{pol, {HighWater: 3000, LowWater: 1500, MaxResultTokens: 100000, InConversation: true}} {
		for _, m := range Build(s.events(t), "sys", 0, p).Messages {
			if strings.Contains(m.Content, "make the change") || strings.Contains(m.Content, DefaultPlanHeader) {
				t.Fatalf("in conversation %v, a hidden plan was shown: %q", p.InConversation, m.Content)
			}
		}
	}
	// A plan shown before it goes too.
	s = newSession()
	s.w.Append(log.KindUserMsg, log.UserMsg{Text: "run turn 1"})
	s.w.Append(log.KindGatePlan, log.GatePlan{Noul: "required", Plan: "1. shown"})
	if sys := Build(s.events(t), "sys", 0, pol).Messages[0].Content; !strings.HasSuffix(sys, "1. shown") {
		t.Fatalf("a plan that is not hidden: %q", sys)
	}
	s.w.Append(log.KindGatePlan, log.GatePlan{Noul: "carried", Plan: "1. shown", Hidden: true})
	if sys := Build(s.events(t), "sys", 0, pol).Messages[0].Content; strings.Contains(sys, "1. shown") {
		t.Fatalf("a hidden plan left the one before it shown: %q", sys)
	}
}
