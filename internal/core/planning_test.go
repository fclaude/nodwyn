package core

import (
	"context"
	"testing"

	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
)

type planningProvider struct{ calls int }

func (*planningProvider) Name() string                         { return "planner" }
func (*planningProvider) Ping(context.Context) (string, error) { return "test", nil }
func (p *planningProvider) Chat(context.Context, llm.Request, func(llm.Delta)) (*llm.Response, error) {
	p.calls++
	return &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: "1. Inspect files."}, Usage: llm.Usage{PromptTokens: 6, OutputTokens: 4}}, nil
}
func TestRequiredPlanningTokensStopBeforeExecution(t *testing.T) {
	o := diskOptions(t)
	p := &planningProvider{}
	o.Provider = p
	o.Config.Budget.TokensSoft = 10
	o.Config.Budget.TokensHard = 10
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SendPlanned("work"); err != nil {
		t.Fatal(err)
	}
	events := (&rig{t: t, s: s}).drain("n")
	outcome := ""
	for _, ev := range events {
		if ev.Kind == string(log.KindTurnEnd) {
			outcome = ev.Payload.(log.TurnEnd).Outcome
		}
		if ev.Kind == string(log.KindToolCall) {
			t.Fatal("host work escaped planning budget")
		}
	}
	if p.calls != 1 || outcome != "hard_budget" {
		t.Fatalf("calls=%d outcome=%s", p.calls, outcome)
	}
}
