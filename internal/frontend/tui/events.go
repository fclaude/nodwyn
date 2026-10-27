package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/frontend/plain"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
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

// worktreeLine says where a session works when it has a git worktree of its
// own (ADR 34), in the console's words. Files the repository has that the
// worktree does not are news, so they are counted.
func worktreeLine(kind log.Kind, p log.WorktreeChange) string {
	switch kind {
	case log.KindWorktreeRestore:
		return fmt.Sprintf("Made its worktree again, on %s, at %s", p.Branch, p.Path)
	case log.KindWorktreeRemove:
		return fmt.Sprintf("Removed its worktree and %s: nothing in them was unlanded", p.Branch)
	}
	commit := p.BaseCommit[:min(7, len(p.BaseCommit))]
	from := "commit " + commit
	if p.Base != "" {
		from = p.Base + " at " + commit
	}
	line := fmt.Sprintf("Working in a git worktree of its own, on %s, from %s, at %s.", p.Branch, from, p.Path)
	switch {
	case p.Uncommitted == 1:
		line += " 1 uncommitted file is in the repository but not here."
	case p.Uncommitted > 1:
		line += fmt.Sprintf(" %d uncommitted files are in the repository but not here.", p.Uncommitted)
	}
	return line
}

// limitsLine is a session.limits record in the conversation: the task's
// limits as set then (ADR 49, amended 2026-10-27).
func limitsLine(p log.SessionLimits) string {
	line := "limits → " + p.Task.String()
	if p.Actor != "" {
		line += " · by " + p.Actor
	}
	return line
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
	m.gauge.observe(e.Kind, e.Payload)
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
		m.dequeue(p.Text)
		m.busy = true
	case string(log.KindLLMResp):
		m.endToolInterval()
		var p log.LLMResp
		p, err = decode[log.LLMResp](e.Payload)
		if p.Thinking != "" {
			m.transcript = append(m.transcript, entry{text: safe("thinking> " + p.Thinking), thinking: true})
		}
		if p.Content != "" {
			m.append("nodwyn> " + p.Content)
		}
		m.append("      " + plain.UsageLine(p.Usage))
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
		if err == nil {
			m.append(formatGatePlan(p))
		}
	case string(log.KindPlanStep):
		var p log.PlanStep
		p, err = decode[log.PlanStep](e.Payload)
		if err == nil {
			line := fmt.Sprintf("step %d of %d marked %s", p.Step, p.Of, p.State)
			if p.Note != "" {
				line += ": " + p.Note
			}
			m.append(line)
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
	case string(log.KindApprovalChange):
		var p log.ApprovalChange
		p, err = decode[log.ApprovalChange](e.Payload)
		if err == nil {
			m.append(strings.Trim(plain.ApprovalSummary(p), "[]") + " by " + p.Actor)
		}
	case string(log.KindSummary):
		var p log.Summary
		p, err = decode[log.Summary](e.Payload)
		if err == nil {
			m.append(fmt.Sprintf("summary seq=%d of %d..%d\n%s", e.Seq, p.From, p.Through, p.Text))
		}
	case string(log.KindSummaryDrop):
		var p log.SummaryDrop
		p, err = decode[log.SummaryDrop](e.Payload)
		if err == nil {
			m.append(fmt.Sprintf("summary %d revoked", p.SummarySeq))
		}
	case string(log.KindBindingChange):
		var p log.BindingChange
		p, err = decode[log.BindingChange](e.Payload)
		if err == nil {
			m.append(plain.BindingSummary(p))
		}
	case string(log.KindNote):
		var p log.Note
		p, err = decode[log.Note](e.Payload)
		m.append("note: " + p.Text)
	case string(log.KindAttach):
		var p log.Attach
		p, err = decode[log.Attach](e.Payload)
		if err == nil {
			m.append(plain.AttachSummary(p))
		}
	case string(log.KindSessionModel):
		var p log.SessionModel
		p, err = decode[log.SessionModel](e.Payload)
		if err == nil {
			m.append(modelLine(p))
		}
	case string(log.KindSessionJudge):
		var p log.SessionJudge
		p, err = decode[log.SessionJudge](e.Payload)
		if err == nil {
			m.append(judgeLine(p))
		}
	case string(log.KindSessionLimits):
		var p log.SessionLimits
		p, err = decode[log.SessionLimits](e.Payload)
		if err == nil {
			m.append(limitsLine(p))
		}
	case string(log.KindWorktreeCreate), string(log.KindWorktreeRestore), string(log.KindWorktreeRemove):
		var p log.WorktreeChange
		p, err = decode[log.WorktreeChange](e.Payload)
		if err == nil {
			m.append(worktreeLine(log.Kind(e.Kind), p))
		}
	case string(log.KindFileVersion):
		var p log.FileVersion
		p, err = decode[log.FileVersion](e.Payload)
		if err == nil {
			m.append("kept the previous version of " + p.Path)
		}
	case string(log.KindDocUpload):
		var p log.DocUpload
		p, err = decode[log.DocUpload](e.Payload)
		if err == nil {
			m.append(plain.DocUploadSummary(p))
		}
	case string(log.KindIndexUpdate):
		// Under the call that built or refreshed the index, which stays
		// open for its result.
		var p log.IndexUpdate
		p, err = decode[log.IndexUpdate](e.Payload)
		if err == nil {
			m.append("  " + plain.IndexUpdateLine(p))
		}
	case string(log.KindHarnessMsg):
		m.endToolInterval()
		var p log.HarnessMsg
		p, err = decode[log.HarnessMsg](e.Payload)
		m.append("nodwyn> " + p.Text)
	case string(log.KindContextNarrow):
		m.endToolInterval()
		var p log.ContextNarrow
		p, err = decode[log.ContextNarrow](e.Payload)
		if p.Lasting {
			m.append(plain.LastingNarrowLine(p))
		} else {
			m.append("── context narrowed ──\n" + p.System)
		}
	case string(log.KindContextWiden):
		m.endToolInterval()
		m.append("── context widened ──")
	case string(log.KindTurnCheckpoint):
		var p log.TurnCheckpoint
		p, err = decode[log.TurnCheckpoint](e.Payload)
		if err == nil {
			m.clearPrompt(p.PromptID)
			m.append("checkpoint " + p.Checkpoint.Dimension + " · " + checkpointDecision(p))
		}
	case string(log.KindCredRegister):
		var p log.CredRegister
		p, err = decode[log.CredRegister](e.Payload)
		m.clearPrompt(p.PromptID)
		m.append("credential registered: " + p.Class)
	case string(log.KindCredRefresh):
		var p log.CredRefresh
		p, err = decode[log.CredRefresh](e.Payload)
		if p.OK {
			m.append("credential refreshed: " + p.Class + " at " + p.Endpoint)
		} else {
			m.append("credential refresh failed: " + p.Class + " at " + p.Endpoint + ": " + p.Error)
		}
	case string(log.KindJevReq):
		var p log.JevReq
		p, err = decode[log.JevReq](e.Payload)
		if err == nil {
			m.append(plain.JudgeLine(p))
		}
	case string(log.KindContextCompact):
		var p log.ContextCompact
		p, err = decode[log.ContextCompact](e.Payload)
		if err == nil {
			m.append(plain.CompactLine(p))
		}
	case string(log.KindMCPEgress):
		var p log.MCPEgress
		p, err = decode[log.MCPEgress](e.Payload)
		if line, ok := plain.EgressLine(p); ok && err == nil {
			m.append(line)
		}
	case event.PromptConfirm, event.PromptSecret, event.PromptCheckpoint, event.PromptChoice:
		err = m.setPrompt(e)
	case event.PromptClosed:
		var p event.Closed
		p, err = decode[event.Closed](e.Payload)
		if err == nil {
			m.clearPrompt(p.ID)
		}
	case string(log.KindSettingChange):
		var p log.SettingChange
		p, err = decode[log.SettingChange](e.Payload)
		if err == nil {
			m.clearPrompt(p.PromptID)
			m.append(plain.SettingSummary(p))
		}
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
			m.queued = nil
			if m.prompt != nil {
				m.clearPrompt(m.prompt.id)
			}
		}
	case string(log.KindTurnEnd), string(log.KindSessionEnd):
		if e.Kind == string(log.KindTurnEnd) {
			var p log.TurnEnd
			if p, err = decode[log.TurnEnd](e.Payload); err == nil && p.Where != "" {
				m.append("where it stopped: " + p.Where)
			}
		}
		m.endToolInterval()
		m.busy = false
		if m.prompt != nil {
			m.clearPrompt(m.prompt.id)
		}
	case "session.closed":
		m.endToolInterval()
		m.disconnected = true
		m.busy = false
		m.status = "Task closed. Reconnect to resume its history."
		if m.prompt != nil {
			m.clearPrompt(m.prompt.id)
		}
	}
	if err != nil {
		m.status = "Invalid " + safe(e.Kind) + " event; reconnect to refresh state."
	}
}
