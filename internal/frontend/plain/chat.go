package plain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
)

// Chat is the line-mode agent front-end: one goroutine reads the user's
// lines and the session's events in turn. While a turn runs, the user's
// only input is the answer to a confirm prompt; Interrupt (Ctrl-C) aborts
// the turn.
type Chat struct {
	Session      *core.Session
	In           io.Reader
	Out          io.Writer
	ShowThinking bool
	// ReplayHistory displays restored history before reading input.
	ReplayHistory bool
	replayedSeq   uint64
	// Interrupt receives a value per Ctrl-C; nil disables.
	Interrupt <-chan struct{}

	lines         *lineReader
	lastWasStream bool
	// Streaming state belongs to this frontend, not the process.
	stream struct {
		mode        string
		atLineStart bool
	}
}

// Run drives the session until :q or EOF.
func (c *Chat) Run(ctx context.Context) error {
	c.Out = TerminalText(c.Out)
	c.lines = newLineReader(c.In, c.Out)
	c.banner()
	if c.ReplayHistory {
		// The whole conversation, from the log: a resumed session holds
		// only its end (ADR 49 §10).
		history, err := c.Session.History()
		if err != nil {
			return err
		}
		Replay(history, c.Out, c.ShowThinking)
		if len(history) > 0 {
			c.replayedSeq = history[len(history)-1].Seq
		}
	}
	// Drain whatever the session emitted before we attached (session.start).
	c.drainIdle()
	for {
		fmt.Fprintln(c.Out)
		line, ok := c.lines.read("you> ")
		if !ok {
			fmt.Fprintln(c.Out)
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") {
			if quit := c.command(ctx, line); quit {
				return nil
			}
			continue
		}
		if err := c.Session.Send(line); err != nil {
			fmt.Fprintln(c.Out, "error:", redact.Redact(err.Error(), nil).Text)
			continue
		}
		c.follow(ctx)
	}
}

func (c *Chat) banner() {
	s := c.Session
	names := make([]string, 0, len(s.Tools()))
	for _, t := range s.Tools() {
		names = append(names, t.Name)
	}
	fmt.Fprintf(c.Out, "nodwyn session %s\nrepo  %s\ntools %s\n:h for commands\n", s.ID(), s.Repo(), strings.Join(names, " "))
}

// drainIdle consumes buffered events without blocking.
func (c *Chat) drainIdle() {
	for {
		select {
		case ev, ok := <-c.Session.Events():
			if !ok {
				return
			}
			if ev.Seq == 0 || ev.Seq > c.replayedSeq {
				c.show(ev)
			}
		default:
			return
		}
	}
}

// follow renders events until the turn ends.
func (c *Chat) follow(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			c.Session.Abort()
		case <-c.Interrupt:
			fmt.Fprintln(c.Out, "\n[aborting]")
			c.Session.Abort()
		case ev, ok := <-c.Session.Events():
			if !ok {
				return
			}
			c.show(ev)
			if ev.Kind == event.Status && ev.Payload.(event.StatusInfo).State == "idle" {
				return
			}
		}
	}
}

// writeStream renders one streamed fragment. Thinking lines are prefixed
// with a dim marker at the start of each line; switching between thinking
// and text starts a fresh line.
func (c *Chat) writeStream(mode, text string) {
	if text == "" {
		return
	}
	if c.stream.mode != mode {
		if c.stream.mode != "" && !c.stream.atLineStart {
			fmt.Fprintln(c.Out)
		}
		c.stream.mode, c.stream.atLineStart = mode, true
	}
	var b strings.Builder
	for _, r := range text {
		if c.stream.atLineStart && mode == "thinking" {
			b.WriteString("· ")
		}
		c.stream.atLineStart = false
		b.WriteRune(r)
		if r == '\n' {
			c.stream.atLineStart = true
		}
	}
	fmt.Fprint(c.Out, b.String())
	c.lastWasStream = true
}

func (c *Chat) show(ev event.Event) {
	endStream := func() {
		if c.lastWasStream {
			if !c.stream.atLineStart {
				fmt.Fprintln(c.Out)
			}
			c.lastWasStream = false
		}
		c.stream.mode, c.stream.atLineStart = "", true
	}
	switch ev.Kind {
	case event.StreamThinking:
		if c.ShowThinking {
			c.writeStream("thinking", ev.Payload.(event.Text).Text)
		}
	case event.StreamText:
		c.writeStream("text", ev.Payload.(event.Text).Text)
	case event.Output:
		c.writeStream("text", ev.Payload.(event.Text).Text)
	case string(log.KindToolCall):
		endStream()
		p := ev.Payload.(log.ToolCallEvent)
		fmt.Fprintf(c.Out, "▸ %s %s\n", p.Name, callLine(p.Name, p.Args))
	case string(log.KindToolResult):
		endStream()
		p := ev.Payload.(log.ToolResult)
		if p.OK {
			fmt.Fprintf(c.Out, "◂ %s ok (%d bytes, %d ms)\n", p.Name, len(p.Value), p.Millis)
		} else {
			fmt.Fprintf(c.Out, "◂ %s error: %s\n", p.Name, p.Error)
		}
	case string(log.KindEvalResult):
		endStream()
		// The value was redacted as it was recorded; a failure is shown by
		// the error event the turn emits.
		if p := ev.Payload.(log.EvalResult); p.Error == "" && p.Value != "" {
			fmt.Fprintln(c.Out, p.Value)
		}
	case string(log.KindIndexUpdate):
		endStream()
		fmt.Fprintln(c.Out, IndexUpdateLine(ev.Payload.(log.IndexUpdate)))
	case string(log.KindGuardDecision):
		p := ev.Payload.(log.GuardDecision)
		// A file an index build left out without asking is named by its
		// index.update line.
		if p.Decision != "allow" && p.Actor != "not asked" {
			endStream()
			fmt.Fprintf(c.Out, "⚑ guard %s %s: %s", p.Decision, p.Prim, p.Reason)
			if p.Answer != "" {
				fmt.Fprintf(c.Out, " → %s", p.Answer)
			}
			fmt.Fprintln(c.Out)
		} else if p.Detail != "" {
			// A standing approval: nobody saw this diff, so show it dimmed.
			endStream()
			fmt.Fprintf(c.Out, "%s\n%s\n", dim("⚑ "+p.Reason), dim(p.Detail))
		}
	case string(log.KindLLMResp):
		endStream()
		p := ev.Payload.(log.LLMResp)
		line := UsageLine(p.Usage)
		if p.Usage.PromptEvalMS > 0 || p.Usage.EvalMS > 0 {
			line += fmt.Sprintf(" (prompt eval %.1fs, gen %.1fs)", float64(p.Usage.PromptEvalMS)/1000, float64(p.Usage.EvalMS)/1000)
		}
		fmt.Fprintf(c.Out, "%s\n", dim("["+line+"]"))
	case string(log.KindLLMReq):
		p := ev.Payload.(log.LLMReq)
		if len(p.Elided) > 0 {
			fmt.Fprintf(c.Out, "%s\n", dim(fmt.Sprintf("[window: %d messages, ~%d tokens, %d elided]", p.Messages, p.EstTokens, len(p.Elided))))
		}
	case event.PromptCheckpoint:
		endStream()
		c.checkpoint(ev.Payload.(event.Checkpoint))
	case event.PromptConfirm:
		endStream()
		c.confirm(ev.Payload.(event.Confirm))
	case event.PromptSecret:
		endStream()
		c.secretInput(ev.Payload.(event.SecretPrompt))
	case event.PromptChoice:
		endStream()
		c.choice(ev.Payload.(event.Choice))
	case string(log.KindSettingChange):
		endStream()
		fmt.Fprintln(c.Out, SettingSummary(ev.Payload.(log.SettingChange)))
	case event.Error:
		endStream()
		fmt.Fprintf(c.Out, "error: %s\n", ev.Payload.(event.ErrorInfo).Err)
	case string(log.KindTurnEnd):
		if where := ev.Payload.(log.TurnEnd).Where; where != "" {
			endStream()
			fmt.Fprintf(c.Out, "where it stopped: %s\n", where)
		}
	case event.Status:
		if ev.Payload.(event.StatusInfo).State == "idle" {
			endStream()
		}
	case string(log.KindBindingChange):
		endStream()
		fmt.Fprintln(c.Out, BindingSummary(ev.Payload.(log.BindingChange)))
	case string(log.KindApprovalChange):
		endStream()
		p := ev.Payload.(log.ApprovalChange)
		fmt.Fprintln(c.Out, ApprovalSummary(p))
	case string(log.KindNote):
		endStream()
		fmt.Fprintf(c.Out, "note: %s\n", ev.Payload.(log.Note).Text)
	case string(log.KindAttach):
		endStream()
		fmt.Fprintln(c.Out, AttachSummary(ev.Payload.(log.Attach)))
	case string(log.KindFileVersion):
		endStream()
		fmt.Fprintf(c.Out, "[kept the previous version of %s]\n", ev.Payload.(log.FileVersion).Path)
	case string(log.KindDocUpload):
		endStream()
		fmt.Fprintln(c.Out, DocUploadSummary(ev.Payload.(log.DocUpload)))
	case string(log.KindContextCompact):
		endStream()
		fmt.Fprintln(c.Out, CompactLine(ev.Payload.(log.ContextCompact)))
	case string(log.KindJevReq):
		endStream()
		fmt.Fprintln(c.Out, dim(JudgeLine(ev.Payload.(log.JevReq))))
	case string(log.KindMCPEgress):
		if line, ok := EgressLine(ev.Payload.(log.MCPEgress)); ok {
			endStream()
			fmt.Fprintln(c.Out, line)
		}
	}
}

// callLine is a call's arguments as line mode shows them: in words for the
// tools CallSummary knows, otherwise as sent, cut.
func callLine(name string, args json.RawMessage) string {
	if s, ok := CallSummary(name, args); ok {
		return s
	}
	return trim(string(args), 200)
}

// DocUploadSummary is the one line a transcript shows for a document sent
// to a document service: where it went and what it became, or why it did
// not go.
func DocUploadSummary(d log.DocUpload) string {
	switch {
	case d.OK:
		return fmt.Sprintf("[sent %s (%d bytes) to %s: %s]", d.Path, d.Bytes, d.Host, d.URL)
	case d.Sent:
		return fmt.Sprintf("[sent %s to %s, which refused it: %s]", d.Path, d.Host, d.Error)
	}
	return fmt.Sprintf("[did not send %s to %s: %s]", d.Path, d.Host, d.Error)
}

// EgressLine is what a transcript shows for a contained MCP server's
// attempt to reach a host (ADR 23 W3b): nothing for one its proxy let
// through, which is routine and stays in the log.
func EgressLine(p log.MCPEgress) (string, bool) {
	switch {
	case p.Dropped > 0:
		return fmt.Sprintf("⚑ %s: %d more connection attempts were not recorded", p.Server, p.Dropped), true
	case !p.Allowed:
		return fmt.Sprintf("⚑ %s could not reach %s:%d: %s", p.Server, p.Host, p.Port, p.Reason), true
	}
	return "", false
}

// AttachSummary is the one line a transcript shows for an attached file.
func AttachSummary(a log.Attach) string {
	if a.Type == "image" {
		return fmt.Sprintf("[attached %s · image · %d×%d · ~%s tokens · goes to %s only]", a.Name, a.Width, a.Height, Thousands(int64(a.Tokens)), a.Destination)
	}
	line := fmt.Sprintf("[attached %s · %s · %d bytes", a.Name, a.Type, a.Bytes)
	if a.Pages > 0 {
		line += fmt.Sprintf(" · %d pages", a.Pages)
	}
	if n := len(a.Redactions); n > 0 {
		line += fmt.Sprintf(" · %d secret-like string(s) hidden", n)
	}
	return line + "]"
}

// CompactLine is what a transcript shows for a compaction: what set it off,
// and the context's size before and after in the window's estimated tokens,
// the unit compaction measures in (a provider may count a fifth fewer).
func CompactLine(p log.ContextCompact) string {
	why := map[string]string{"auto": "past ~" + Thousands(int64(p.Threshold)) + " tokens", "command": "/compact", "requested": "asked for by the strategy"}[p.Trigger]
	if why == "" {
		why = p.Trigger
	}
	if !p.Compacted {
		return fmt.Sprintf("⚑ compaction (%s) gave up: no handover note came back; the turn goes on in the full context of ~%s tokens", why, Thousands(int64(p.Before)))
	}
	line := fmt.Sprintf("── compacted (%s): context ~%s → ~%s tokens, handover note %s tokens", why, Thousands(int64(p.Before)), Thousands(int64(p.After)), Thousands(int64(p.NoteTokens)))
	if p.Truncated {
		line += ", cut short at the output limit"
	}
	return line + " ──"
}

// LastingNarrowLine is the line a transcript shows for a lasting
// narrowing, such as a compaction's, in place of its system prompt, which is
// the session's own: later turns start here.
func LastingNarrowLine(p log.ContextNarrow) string {
	name := p.Agent
	if name == "" {
		name = "context narrowed"
	}
	return "── " + name + ": the conversation goes on from here ──"
}

func (c *Chat) confirm(p event.Confirm) {
	fmt.Fprintf(c.Out, "\n─── confirm: %s %s\n", p.Prompt.Prim, p.Prompt.Args)
	if scope := EgressSummary(p.Prompt.Egress); scope != "" {
		fmt.Fprintln(c.Out, indent(scope, "  "))
	}
	for _, r := range p.Prompt.Reasons {
		fmt.Fprintf(c.Out, "  • %s\n", r)
	}
	if p.Prompt.Detail != "" {
		fmt.Fprintln(c.Out, indent(p.Prompt.Detail, "  "))
	}
	options := p.Prompt.ApprovalOptions()
	line, ok := c.lines.answer(approvalQuestion(options))
	if !ok {
		line = "n"
	}
	// The wider answers are the explicit scope operations the TUI uses, only
	// for what the guard offered; anything else, however typed, declines.
	var err error
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		err = c.Session.AnswerFrom(p.ID, "y", "terminal")
	case "b":
		if err = offered(options.Binding); err == nil {
			err = c.Session.CreateBinding(p.ID, "terminal")
		}
	case "a":
		if err = offered(options.Session); err == nil {
			err = c.Session.CreateApproval(p.ID, "session", "terminal")
		}
	case "t":
		if err = offered(options.Turn); err == nil {
			err = c.Session.CreateApproval(p.ID, "turn", "terminal")
		}
	default:
		err = c.Session.AnswerFrom(p.ID, "n", "terminal")
	}
	if err != nil {
		fmt.Fprintf(c.Out, "approval: %s\n", err)
		_ = c.Session.AnswerFrom(p.ID, "n", "terminal")
	}
}

// offered is the error for a wider answer the prompt did not offer, nil when
// it did.
func offered(ok bool) error {
	if ok {
		return nil
	}
	return errors.New("that answer is not offered for this prompt; declined")
}

// approvalQuestion is the confirm's question, listing the answers the guard
// offers beyond y and n (guard.Prompt.ApprovalOptions), as the TUI does.
func approvalQuestion(o guard.ApprovalOptions) string {
	choices := "y/N"
	if o.Binding {
		choices += "/b=bind this exact scope persistently and allow"
	}
	if o.Session {
		if o.Kind == "command" {
			choices += fmt.Sprintf("/a=always %s this session", o.Target)
		} else {
			choices += fmt.Sprintf("/a=always under %s", o.Target)
		}
	}
	if o.Turn {
		choices += "/t=all writes this turn"
	}
	return "allow? [" + choices + "] "
}

func (c *Chat) secretInput(p event.SecretPrompt) {
	fmt.Fprintf(c.Out, "\n─── credential-register: %s\n", p.Class)
	line, ok := c.lines.secret("secret (masked, empty to cancel): ")
	if !ok {
		line = ""
	}
	_ = c.Session.SecretReply(p.ID, []byte(line))
	if line == "" {
		fmt.Fprintln(c.Out, "cancelled")
	}
}

func (c *Chat) command(ctx context.Context, line string) (quit bool) {
	fields := strings.Fields(line)
	switch fields[0] {
	case ":q", ":quit", ":exit":
		return true
	case ":h", ":help":
		fmt.Fprint(c.Out, `commands:
  :q             quit
  :log [n]       show the last n log events (default 20, at most 1000)
  :tools         list the tools offered to the model
  :eval EXPR     evaluate Scheme with user origin
  :thinking      toggle showing the model's thinking
  :allow         list standing command and write approvals in effect
`)
	case ":log":
		n := 20
		if len(fields) > 1 {
			var err error
			if n, err = strconv.Atoi(fields[1]); err != nil || n < 1 || n > 1000 {
				fmt.Fprintln(c.Out, "usage: :log [n], n a count of events from 1 to 1000")
				return false
			}
		}
		// The durable log, as the TUI's :log reads it, so a resumed session
		// shows the events of the runs before this one too.
		history, err := c.Session.History()
		if err != nil {
			fmt.Fprintln(c.Out, "error:", redact.Redact(err.Error(), nil).Text)
			return false
		}
		for _, ev := range history[max(0, len(history)-n):] {
			fmt.Fprintf(c.Out, "%5d %-15s %s\n", ev.Seq, ev.Kind, trim(string(ev.Payload), 120))
		}
	case ":tools":
		for _, t := range c.Session.Tools() {
			fmt.Fprintf(c.Out, "%-12s %s\n", t.Name, t.Description)
		}
	case ":eval":
		src := strings.TrimSpace(strings.TrimPrefix(line, ":eval"))
		// The same recorded, serialized path as the TUI and the console, so
		// the log shows what ran; following its events shows a prompt it
		// raises (credential-register's masked input, a confirm) and answers
		// it instead of waiting for a reply read only afterwards.
		if err := c.Session.StartEval(src, "line"); err != nil {
			fmt.Fprintln(c.Out, "error:", redact.Redact(err.Error(), nil).Text)
		} else {
			c.follow(ctx)
		}
	case ":allow":
		for _, a := range c.Session.Allowed() {
			fmt.Fprintln(c.Out, "command", a)
		}
		for _, a := range c.Session.AllowedWrites() {
			fmt.Fprintln(c.Out, "write  ", a)
		}
	case ":thinking":
		c.ShowThinking = !c.ShowThinking
		fmt.Fprintf(c.Out, "thinking: %v\n", c.ShowThinking)
	default:
		fmt.Fprintln(c.Out, "unknown command; :h for help")
	}
	return false
}

func trim(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > n {
		return s[:n-3] + "..."
	}
	return s
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// TerminalText writes to w with the control characters a terminal acts on
// left out, newlines and tabs kept. What line mode prints (a reply, a diff,
// a command's output, a file's name) is the model's or a repository's, and
// an escape sequence in it could rewrite the screen, set the window's title
// or answer a terminal query as if typed. Plain mode writes no colours of its
// own, so nothing it means is lost.
func TerminalText(w io.Writer) io.Writer {
	if t, ok := w.(terminalText); ok {
		return t
	}
	return terminalText{w}
}

type terminalText struct{ w io.Writer }

func (t terminalText) Write(p []byte) (int, error) {
	clean := strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, string(p))
	if _, err := io.WriteString(t.w, clean); err != nil {
		return 0, err
	}
	return len(p), nil
}

// dim marks secondary text. Plain mode has no colours; the marker is a
// leading middle dot so it stays legible in a log file.
func dim(s string) string {
	if s == "" {
		return s
	}
	return "· " + strings.ReplaceAll(s, "\n", "\n· ")
}

// Replay prints a session log as a transcript.
func Replay(events []log.Event, w io.Writer, showThinking bool) {
	w = TerminalText(w)
	for _, ev := range events {
		switch ev.Kind {
		case log.KindSessionStart:
			var p log.SessionStart
			_ = ev.Decode(&p)
			fmt.Fprintf(w, "# session %s — %s %s @ %s (%d tools)\n", ev.Session, p.Model, p.Provider, p.Repo, p.Tools)
		case log.KindEvalReq:
			var p log.EvalRequest
			_ = ev.Decode(&p)
			fmt.Fprintf(w, "\nscheme> %s\n", p.Source)
		case log.KindEvalResult:
			var p log.EvalResult
			_ = ev.Decode(&p)
			if p.Error != "" {
				fmt.Fprintf(w, "eval error: %s\n", p.Error)
			} else if p.Value != "" {
				fmt.Fprintln(w, p.Value)
			}
		case log.KindSummary:
			var p log.Summary
			_ = ev.Decode(&p)
			fmt.Fprintf(w, "[summary seq=%d of %d..%d]\n%s\n", ev.Seq, p.From, p.Through, p.Text)
		case log.KindSummaryDrop:
			var p log.SummaryDrop
			_ = ev.Decode(&p)
			fmt.Fprintf(w, "[summary %d revoked]\n", p.SummarySeq)
		case log.KindBindingChange:
			var p log.BindingChange
			_ = ev.Decode(&p)
			fmt.Fprintln(w, BindingSummary(p))
		case log.KindApprovalChange:
			var p log.ApprovalChange
			_ = ev.Decode(&p)
			if p.Kind == "all" {
				fmt.Fprintf(w, "%s by %s\n", ApprovalSummary(p), p.Actor)
			} else {
				fmt.Fprintf(w, "[approval %s %s %s by %s]\n", p.Scope, p.Kind, p.Prefix, p.Actor)
			}

		case log.KindUserMsg:
			var p log.UserMsg
			_ = ev.Decode(&p)
			fmt.Fprintf(w, "\nyou> %s\n", p.Text)
		case log.KindHarnessMsg:
			var p log.HarnessMsg
			_ = ev.Decode(&p)
			fmt.Fprintf(w, "nodwyn> %s\n", p.Text)
		case log.KindContextNarrow:
			var p log.ContextNarrow
			_ = ev.Decode(&p)
			if p.Lasting {
				fmt.Fprintln(w, LastingNarrowLine(p))
			} else {
				fmt.Fprintf(w, "── context narrowed ──\n%s\n", dim(p.System))
			}
		case log.KindContextWiden:
			fmt.Fprintln(w, "── context widened ──")
		case log.KindContextCompact:
			var p log.ContextCompact
			_ = ev.Decode(&p)
			fmt.Fprintln(w, CompactLine(p))
		case log.KindJevReq:
			var p log.JevReq
			_ = ev.Decode(&p)
			fmt.Fprintln(w, dim(JudgeLine(p)))
		case log.KindLLMResp:
			var p log.LLMResp
			_ = ev.Decode(&p)
			if showThinking && p.Thinking != "" {
				fmt.Fprintln(w, dim(p.Thinking))
			}
			if p.Content != "" {
				fmt.Fprintln(w, p.Content)
			}
			for _, tc := range p.ToolCalls {
				fmt.Fprintf(w, "▸ %s %s\n", tc.Name, callLine(tc.Name, tc.Args))
			}
		case log.KindToolResult:
			var p log.ToolResult
			_ = ev.Decode(&p)
			if p.OK {
				fmt.Fprintf(w, "◂ %s ok (%d bytes)\n", p.Name, len(p.Value))
			} else {
				fmt.Fprintf(w, "◂ %s error: %s\n", p.Name, p.Error)
			}
		case log.KindIndexUpdate:
			var p log.IndexUpdate
			_ = ev.Decode(&p)
			fmt.Fprintln(w, IndexUpdateLine(p))
		case log.KindGuardDecision:
			var p log.GuardDecision
			_ = ev.Decode(&p)
			if p.Decision != "allow" && p.Actor != "not asked" {
				fmt.Fprintf(w, "⚑ guard %s %s: %s %s\n", p.Decision, p.Prim, p.Reason, p.Answer)
			}
		case log.KindNote:
			var p log.Note
			_ = ev.Decode(&p)
			fmt.Fprintf(w, "note: %s\n", p.Text)
		case log.KindAttach:
			var p log.Attach
			_ = ev.Decode(&p)
			fmt.Fprintln(w, AttachSummary(p))
		case log.KindFileVersion:
			var p log.FileVersion
			_ = ev.Decode(&p)
			fmt.Fprintf(w, "[kept the previous version of %s]\n", p.Path)
		case log.KindDocUpload:
			var p log.DocUpload
			_ = ev.Decode(&p)
			fmt.Fprintln(w, DocUploadSummary(p))
		case log.KindMCPEgress:
			var p log.MCPEgress
			_ = ev.Decode(&p)
			if line, ok := EgressLine(p); ok {
				fmt.Fprintln(w, line)
			}
		case log.KindTurnCheckpoint:
			var p log.TurnCheckpoint
			ev.Decode(&p)
			fmt.Fprintf(w, "[checkpoint %s: continue=%t, soft=%s]\n", p.Checkpoint.Dimension, p.Choice.Continue, budget.Amount(p.Checkpoint.Dimension, p.Choice.Soft))
		case log.KindSessionEnd:
			fmt.Fprintf(w, "\n# end (%s)\n", ev.TS.Format(time.RFC3339))
		}
	}
}

// choice asks a question with a fixed set of answers, each named by its key.
func (c *Chat) choice(ev event.Choice) {
	fmt.Fprintf(c.Out, "\n─── %s\n", ev.Title)
	for _, r := range ev.Reasons {
		fmt.Fprintf(c.Out, "  %s\n", r)
	}
	keys := make([]string, len(ev.Choices))
	for i, o := range ev.Choices {
		keys[i] = o.Key
		fmt.Fprintf(c.Out, "  %-6s %s: %s\n", o.Key, o.Label, o.Detail)
	}
	for {
		line, ok := c.lines.answer("[" + strings.Join(keys, "/") + "] ")
		if !ok {
			// Nobody is left to answer, and the session closes only when
			// the turn waiting on this answer has ended: stop the turn.
			c.Session.Abort()
			return
		}
		line = strings.TrimSpace(line)
		if !slices.Contains(keys, line) {
			fmt.Fprintf(c.Out, "answer one of %s\n", strings.Join(keys, ", "))
			continue
		}
		if err := c.Session.AnswerFrom(ev.ID, line, "terminal"); err != nil {
			fmt.Fprintln(c.Out, err)
		}
		return
	}
}

// SettingSummary is the one line a transcript shows for a changed setting.
func SettingSummary(p log.SettingChange) string {
	if p.State != "applied" {
		return fmt.Sprintf("[%s = %s was not saved: %s]", p.Key, p.Value, p.Error)
	}
	return fmt.Sprintf("[%s = %s, saved in config.toml]", p.Key, p.Value)
}

func (c *Chat) checkpoint(ev event.Checkpoint) {
	p := ev.Checkpoint
	fmt.Fprintf(c.Out, "\n─── checkpoint: %s %s (soft %s, hard %s)\n", p.Dimension, budget.Amount(p.Dimension, p.Used), budget.Amount(p.Dimension, p.Limit.Soft), budget.Amount(p.Dimension, p.Limit.Hard))
	spend := ""
	if s := SpendSoFar(p.State); s != "" {
		spend = " · " + s
	}
	fmt.Fprintf(c.Out, "  %d tool calls, %d tokens, %d actions, %s elapsed%s\n", p.State.ToolCalls, p.State.Tokens, p.State.Actions, p.State.Elapsed.Round(time.Second), spend)
	if j := JudgeSoFar(p.State); j != "" {
		fmt.Fprintf(c.Out, "  %s\n", j)
	}
	if len(p.State.Files) > 0 {
		fmt.Fprintf(c.Out, "  files: %s\n", strings.Join(p.State.Files, ", "))
	}
	if len(p.State.Commands) > 0 {
		fmt.Fprintf(c.Out, "  commands: %s\n", strings.Join(p.State.Commands, "; "))
	}
	for {
		line, ok := c.lines.answer("continue? [y=double soft/N/absolute soft limit] ")
		if !ok {
			line = "n"
		}
		if _, err := budget.ParseChoice(line, p); err != nil {
			fmt.Fprintln(c.Out, err)
			continue
		}
		if err := c.Session.AnswerFrom(ev.ID, line, "terminal"); err != nil {
			fmt.Fprintln(c.Out, err)
		}
		return
	}
}
