package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/frontend/rpc"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
)

// Daemon and repository text is content, never terminal control sequences.
func safe(s string) string {
	s = ansi.Strip(redact.Redact(s, nil).Text)
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}
func clip(s string, width int) string {
	return ansi.Truncate(strings.ReplaceAll(s, "\n", " "), max(1, width), "…")
}
func decode[T any](raw json.RawMessage) (T, error) {
	var p T
	err := json.Unmarshal(raw, &p)
	return p, err
}
func (m *model) consume(e rpc.ClientEvent) {
	if e.ID != m.id {
		return
	}
	if e.Seq != 0 {
		if e.Seq <= m.lastSeq {
			return
		}
		m.lastSeq = e.Seq
	}
	var err error
	switch e.Kind {
	case event.StreamText, event.StreamThinking, event.Output:
		var p event.Text
		p, err = decode[event.Text](e.Payload)
		if e.Kind == event.StreamText {
			m.liveText += p.Text
		} else if e.Kind == event.StreamThinking {
			m.liveThinking += p.Text
		} else {
			m.append(p.Text)
		}
	case string(log.KindUserMsg):
		m.endToolInterval()
		var p log.UserMsg
		p, err = decode[log.UserMsg](e.Payload)
		m.append("\nyou> " + p.Text)
		m.busy = true
	case string(log.KindLLMResp):
		m.endToolInterval()
		var p log.LLMResp
		p, err = decode[log.LLMResp](e.Payload)
		if p.Thinking != "" {
			m.transcript = append(m.transcript, entry{text: safe("thinking> " + p.Thinking), thinking: true})
		}
		if p.Content != "" {
			m.append("assistant> " + p.Content)
		}
		m.append(fmt.Sprintf("[%d prompt / %d output tokens]", p.Usage.PromptTokens, p.Usage.OutputTokens))
		m.liveText = ""
		m.liveThinking = ""
	case string(log.KindToolCall):
		var p log.ToolCallEvent
		p, err = decode[log.ToolCallEvent](e.Payload)
		if err == nil {
			m.startToolCall(p)
		} else {
			m.endToolInterval()
		}
	case string(log.KindToolResult):
		var p log.ToolResult
		p, err = decode[log.ToolResult](e.Payload)
		if err == nil {
			m.recordToolResult(p)
		}
	case string(log.KindGuardDecision):
		var p log.GuardDecision
		p, err = decode[log.GuardDecision](e.Payload)
		if err == nil {
			grouped := m.recordToolDecision(p)
			if !grouped && (p.Decision != "allow" || p.Detail != "") {
				m.append("⚑ " + p.Prim + " · " + p.Decision + " " + p.Answer + " · " + p.Reason + "\n" + p.Detail)
			}
			if p.PromptID != "" {
				m.clearPrompt(p.PromptID)
			}
		}
	case string(log.KindGatePlan):
		var p log.GatePlan
		p, err = decode[log.GatePlan](e.Payload)
		if p.Plan != "" {
			m.append(fmt.Sprintf("plan · %.2f\n%s", p.Score, p.Plan))
		}
	case string(log.KindEvalReq):
		m.endToolInterval()
		var p log.EvalRequest
		p, err = decode[log.EvalRequest](e.Payload)
		m.append("\nscheme> " + p.Source)
		m.frictionRequest(p.Source)
		m.busy = true
	case string(log.KindEvalResult):
		var p log.EvalResult
		p, err = decode[log.EvalResult](e.Payload)
		if err == nil {
			m.frictionResult(p)
		}
		if p.Error != "" {
			m.append("eval error: " + p.Error)
		} else if p.Value != "" {
			m.append(p.Value)
		}
	case string(log.KindNote):
		var p log.Note
		p, err = decode[log.Note](e.Payload)
		m.append("note: " + p.Text)
	case string(log.KindTurnCheckpoint):
		var p log.TurnCheckpoint
		p, err = decode[log.TurnCheckpoint](e.Payload)
		m.clearPrompt(p.PromptID)
		m.append(fmt.Sprintf("checkpoint %s · continue=%t", p.Checkpoint.Dimension, p.Choice.Continue))
	case string(log.KindCredRegister):
		var p log.CredRegister
		p, err = decode[log.CredRegister](e.Payload)
		m.clearPrompt(p.PromptID)
		m.append("credential registered: " + p.Class)
	case event.PromptConfirm, event.PromptSecret, event.PromptCheckpoint:
		err = m.setPrompt(e)
	case event.Error:
		var p event.ErrorInfo
		p, err = decode[event.ErrorInfo](e.Payload)
		m.append("error: " + p.Err)
	case event.Status:
		var p event.StatusInfo
		p, err = decode[event.StatusInfo](e.Payload)
		if p.State == "idle" {
			m.endToolInterval()
			m.busy = false
			if m.prompt != nil {
				m.clearPrompt(m.prompt.id)
			}
		}
	case string(log.KindTurnEnd), string(log.KindSessionEnd):
		m.endToolInterval()
		m.busy = false
		if m.prompt != nil {
			m.clearPrompt(m.prompt.id)
		}
	case "session.closed":
		m.endToolInterval()
		m.disconnected = true
		m.busy = false
		m.status = "Session closed. Reconnect to resume its history."
		if m.prompt != nil {
			m.clearPrompt(m.prompt.id)
		}
	}
	if err != nil {
		m.status = "Invalid " + safe(e.Kind) + " event; reconnect to refresh state."
	}
}
