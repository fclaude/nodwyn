package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/log"
)

func testModel() *model {
	m := newModel(context.Background(), nil, "/repo", "session", false)
	m.active = true
	return m
}
func TestSecretInputNeverEntersViewOrTranscript(t *testing.T) {
	m := testModel()
	m.composer.SetValue("unfinished\nmessage")
	ev := wire(event.PromptSecret, event.SecretPrompt{ID: "secret1", Class: "service"}, 0)
	m.consume(ev)
	m.promptInput.SetValue("unrecognized-canary-secret")
	if m.promptInput.EchoMode != textinput.EchoPassword {
		t.Fatal("secret not masked")
	}
	if strings.Contains(m.View().Content, "unrecognized-canary-secret") || strings.Contains(fmt.Sprint(m.transcript), "unrecognized-canary-secret") {
		t.Fatal("secret visible")
	}
	m.consume(ev) // Duplicate snapshot/live prompt must not erase partially typed input.
	if m.promptInput.Value() != "unrecognized-canary-secret" {
		t.Fatal("duplicate prompt reset input")
	}
	m.consume(wire(string(log.KindCredRegister), log.CredRegister{PromptID: "secret1", Class: "service"}, 1))
	if m.prompt != nil || m.composer.Value() != "unfinished\nmessage" || m.promptInput.Value() != "" || m.promptInput.EchoMode != textinput.EchoNormal {
		t.Fatal("credential completion failed to restore draft")
	}
	m.consume(ev)
	if m.prompt != nil {
		t.Fatal("answered prompt reopened")
	}
}
func TestSecurityPromptNeverOffersWiderScope(t *testing.T) {
	m := testModel()
	m.consume(wire(event.PromptConfirm, event.Confirm{ID: "p", Prompt: guard.Prompt{Prim: "fs-write", Path: "notes/a", Security: true}}, 0))
	for _, answer := range []string{"a", "t"} {
		if cmd := m.reply(answer); cmd != nil {
			t.Fatal("security prompt created grant")
		}
	}
	if m.prompt.session || m.prompt.turn || strings.Contains(m.prompt.choices, "a =") {
		t.Fatal("security prompt offers standing authority")
	}
}
func TestDurableResponseReplacesStreamAndDeduplicatesHistory(t *testing.T) {
	m := testModel()
	m.consume(wire(event.StreamText, event.Text{Text: "hello"}, 0))
	m.refresh(false)
	if !strings.Contains(m.View().Content, "hello") {
		t.Fatal("stream missing")
	}
	e := wire(string(log.KindLLMResp), log.LLMResp{Content: "hello"}, 1)
	m.consume(e)
	m.consume(e)
	m.refresh(false)
	if strings.Count(m.View().Content, "hello") != 1 {
		t.Fatal("stream or duplicate history repeated response")
	}
}
func TestContentCannotControlTerminalAndDiffScrolls(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 16})
	detail := strings.Repeat("long diff line with a complete visible suffix\n", 40) + "last line"
	m.consume(wire(event.PromptConfirm, event.Confirm{ID: "p", Prompt: guard.Prompt{Prim: "fs-write", Path: "notes/a", Detail: "\x1b]52;c;ATTACK\a\x1b[2J" + detail}}, 0))
	if strings.Contains(m.prompt.detail, "\x1b") || strings.Contains(m.prompt.detail, "ATTACK") {
		t.Fatal("terminal sequence survived")
	}
	if m.viewport.AtBottom() {
		t.Fatal("long prompt started at bottom")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.viewport.AtTop() {
		t.Fatal("diff cannot scroll")
	}
	if !m.viewport.SoftWrap {
		t.Fatal("long diff lines truncated")
	}
	m.Update(tea.WindowSizeMsg{Width: 10, Height: 4})
	if !strings.Contains(m.View().Content, "Resize") {
		t.Fatal("small terminal fallback missing")
	}
}
func TestFailedSendAndDisconnectRemainRecoverable(t *testing.T) {
	m := testModel()
	m.busy = true
	m.working = true
	m.Update(actionResult{kind: "send", err: errors.New("failed")})
	if m.working || m.busy {
		t.Fatal("failed send left interface busy forever")
	}
	m.consume(wire(event.PromptSecret, event.SecretPrompt{ID: "p", Class: "test"}, 0))
	m.promptInput.SetValue("canary")
	m.Update(nextResult{err: errors.New("EOF"), generation: m.generation})
	if !m.disconnected || m.prompt != nil || m.promptInput.Value() != "" {
		t.Fatal("disconnect retained secret or stale prompt")
	}
}

func TestThinkingToggleRevealsAndHidesRecordedReasoning(t *testing.T) {
	m := testModel()
	m.consume(wire(string(log.KindLLMResp), log.LLMResp{Content: "answer", Thinking: "reasoning"}, 1))
	m.refresh(false)
	if strings.Contains(m.View().Content, "reasoning") {
		t.Fatal("thinking shown by default")
	}
	m.composer.SetValue(":thinking")
	m.submit()
	if !strings.Contains(m.View().Content, "reasoning") {
		t.Fatal("thinking toggle cannot reveal history")
	}
	m.composer.SetValue(":thinking")
	m.submit()
	if strings.Contains(m.View().Content, "reasoning") {
		t.Fatal("thinking toggle cannot hide history")
	}
}

func TestEgressPromptShowsScopeWithoutOfferingBindingGrant(t *testing.T) {
	m := testModel()
	m.consume(wire(event.PromptConfirm, event.Confirm{ID: "egress", Prompt: guard.Prompt{Prim: "http-request", Egress: &guard.EgressScope{Class: "service", Host: "api.example.test", Method: "POST", Position: "body"}}}, 0))
	for _, want := range []string{`Credential class: "service"`, `Destination host: "api.example.test"`, `Method: "POST"`, `Position: "body"`} {
		if !strings.Contains(m.prompt.detail, want) {
			t.Fatalf("missing %q in %s", want, m.prompt.detail)
		}
	}
	for _, answer := range []string{"a", "t", "bind"} {
		if m.reply(answer) != nil {
			t.Fatalf("%q granted authority", answer)
		}
	}
	if m.prompt.session || m.prompt.turn {
		t.Fatal("scope offered standing authority")
	}
}

func TestBindingChoiceRequiresProposalAndKeepsSecurityPromptNarrow(t *testing.T) {
	for _, security := range []bool{false, true} {
		m := testModel()
		row := binding.Binding{Class: "service", Dest: "api.test", Methods: []string{"POST"}, Positions: []string{"body"}}
		m.consume(wire(event.PromptConfirm, event.Confirm{ID: "p", Prompt: guard.Prompt{Security: security, Binding: &row, Egress: &guard.EgressScope{Class: "service", Host: "api.test", Method: "POST", Position: "body"}}}, 0))
		if m.prompt.bind == security {
			t.Fatal("wrong binding eligibility")
		}
		cmd := m.reply("b")
		if (cmd != nil) == security {
			t.Fatal("wrong binding action")
		}
	}
}

func TestApprovalAuditIsVisibleAndDeduplicated(t *testing.T) {
	m := testModel()
	ev := wire(string(log.KindApprovalChange), log.ApprovalChange{PromptID: "p", Scope: "session", Kind: "write", Prefix: "notes/", Actor: "terminal"}, 1)
	m.consume(ev)
	m.consume(ev)
	m.refresh(false)
	want := "approval requested: session write notes/ by terminal"
	if strings.Count(m.View().Content, want) != 1 {
		t.Fatal("approval audit missing or duplicated", m.View().Content)
	}
	if m.prompt != nil {
		t.Fatal("replay recreated approval")
	}
}
