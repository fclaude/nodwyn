package plain

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/scheme"
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
	recent        []event.Event
	lastWasStream bool
	// Streaming state belongs to this frontend, not the process.
	stream struct {
		mode        string
		atLineStart bool
	}
}

// Run drives the session until :q or EOF.
func (c *Chat) Run(ctx context.Context) error {
	c.lines = newLineReader(c.In, c.Out)
	c.banner()
	if c.ReplayHistory {
		history := c.Session.EventsSoFar()
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

// evalFollowing evaluates src while rendering the session's events, so a
// prompt the evaluation raises (credential-register's masked input, a
// confirm) is shown and answered instead of waiting for a reply that the
// front-end would only read after Eval returned.
func (c *Chat) evalFollowing(ctx context.Context, src string) (scheme.Value, error) {
	type result struct {
		v   scheme.Value
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := c.Session.Eval(ctx, src)
		done <- result{v, err}
	}()
	for {
		select {
		case r := <-done:
			c.drainIdle()
			return r.v, r.err
		case ev, ok := <-c.Session.Events():
			if !ok {
				r := <-done
				return r.v, r.err
			}
			if ev.Seq == 0 || ev.Seq > c.replayedSeq {
				c.show(ev)
			}
		}
	}
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
	if ev.Seq != 0 {
		c.recent = append(c.recent, ev)
		if len(c.recent) > 200 {
			c.recent = c.recent[len(c.recent)-200:]
		}
	}
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
		fmt.Fprintf(c.Out, "▸ %s %s\n", p.Name, trim(string(p.Args), 200))
	case string(log.KindToolResult):
		endStream()
		p := ev.Payload.(log.ToolResult)
		if p.OK {
			fmt.Fprintf(c.Out, "◂ %s ok (%d bytes, %d ms)\n", p.Name, len(p.Value), p.Millis)
		} else {
			fmt.Fprintf(c.Out, "◂ %s error: %s\n", p.Name, p.Error)
		}
	case string(log.KindGuardDecision):
		p := ev.Payload.(log.GuardDecision)
		if p.Decision != "allow" {
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
		fmt.Fprintf(c.Out, "%s\n", dim(fmt.Sprintf("[%d prompt tokens, %d out, prompt eval %.1fs, gen %.1fs]",
			p.Usage.PromptTokens, p.Usage.OutputTokens, float64(p.Usage.PromptEvalMS)/1000, float64(p.Usage.EvalMS)/1000)))
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
	case event.Error:
		endStream()
		fmt.Fprintf(c.Out, "error: %s\n", ev.Payload.(event.ErrorInfo).Err)
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
		fmt.Fprintf(c.Out, "[approval requested: %s %s %s]\n", p.Scope, p.Kind, p.Prefix)
	case string(log.KindNote):
		endStream()
		fmt.Fprintf(c.Out, "note: %s\n", ev.Payload.(log.Note).Text)
	case string(log.KindAttach):
		endStream()
		fmt.Fprintln(c.Out, AttachSummary(ev.Payload.(log.Attach)))
	case string(log.KindFileVersion):
		endStream()
		fmt.Fprintf(c.Out, "[kept the previous version of %s]\n", ev.Payload.(log.FileVersion).Path)
	}
}

// AttachSummary is the one line a transcript shows for an attached file.
func AttachSummary(a log.Attach) string {
	if a.Type == "image" {
		tokens := fmt.Sprint(a.Tokens)
		for i := len(tokens) - 3; i > 0; i -= 3 {
			tokens = tokens[:i] + "," + tokens[i:]
		}
		return fmt.Sprintf("[attached %s · image · %d×%d · ~%s tokens · goes to %s only]", a.Name, a.Width, a.Height, tokens, a.Destination)
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
	prompt := "allow? [y/N] "
	switch {
	case p.Prompt.Security:
		// Security confirmations always apply to this call only.
	case len(p.Prompt.Argv) > 0:
		prompt = "allow? [y/N/a=always this session] "
	case p.Prompt.Path != "" && !guard.IsProtected(p.Prompt.Path):
		prompt = fmt.Sprintf("allow? [y/N/a=always under %s/t=all writes this turn] ", guard.DirPrefix(p.Prompt.Path))
	}
	if p.Prompt.Binding != nil && !p.Prompt.Security {
		prompt = "allow? [y/N/b=bind this exact scope persistently and allow] "
	}
	line, ok := c.lines.read(prompt)
	if !ok {
		line = "n"
	}
	if (strings.EqualFold(strings.TrimSpace(line), "b")) && p.Prompt.Binding != nil && !p.Prompt.Security {
		if err := c.Session.CreateBinding(p.ID, "terminal"); err != nil {
			fmt.Fprintf(c.Out, "binding: %s\n", err)
			_ = c.Session.Answer(p.ID, "n")
		}
	} else {
		_ = c.Session.Answer(p.ID, line)
	}
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
  :log [n]       show the last n log events (default 20)
  :tools         list the tools offered to the model
  :eval EXPR     evaluate Scheme with user origin
  :thinking      toggle showing the model's thinking
  :allow         list standing command and write approvals in effect
`)
	case ":log":
		n := 20
		if len(fields) > 1 {
			n, _ = strconv.Atoi(fields[1])
		}
		start := len(c.recent) - n
		if start < 0 {
			start = 0
		}
		for _, ev := range c.recent[start:] {
			fmt.Fprintf(c.Out, "%5d %-15s %s\n", ev.Seq, ev.Kind, trim(fmt.Sprintf("%+v", ev.Payload), 120))
		}
	case ":tools":
		for _, t := range c.Session.Tools() {
			fmt.Fprintf(c.Out, "%-12s %s\n", t.Name, t.Description)
		}
	case ":eval":
		src := strings.TrimSpace(strings.TrimPrefix(line, ":eval"))
		v, err := c.evalFollowing(ctx, src)
		if err != nil {
			fmt.Fprintln(c.Out, "error:", redact.Redact(err.Error(), nil).Text)
		} else if v.Kind != scheme.KVoid {
			fmt.Fprintln(c.Out, redact.Redact(scheme.WriteWithStrings(v, func(text string) string {
				return redact.Redact(text, nil).Text
			}), nil).Text)
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
			fmt.Fprintf(w, "[approval %s %s %s by %s]\n", p.Scope, p.Kind, p.Prefix, p.Actor)

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
				fmt.Fprintf(w, "▸ %s %s\n", tc.Name, trim(string(tc.Args), 200))
			}
		case log.KindToolResult:
			var p log.ToolResult
			_ = ev.Decode(&p)
			if p.OK {
				fmt.Fprintf(w, "◂ %s ok (%d bytes)\n", p.Name, len(p.Value))
			} else {
				fmt.Fprintf(w, "◂ %s error: %s\n", p.Name, p.Error)
			}
		case log.KindGuardDecision:
			var p log.GuardDecision
			_ = ev.Decode(&p)
			if p.Decision != "allow" {
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
		case log.KindTurnCheckpoint:
			var p log.TurnCheckpoint
			ev.Decode(&p)
			fmt.Fprintf(w, "[checkpoint %s: continue=%t, soft=%s]\n", p.Checkpoint.Dimension, p.Choice.Continue, budgetAmount(p.Checkpoint.Dimension, p.Choice.Soft))
		case log.KindSessionEnd:
			fmt.Fprintf(w, "\n# end (%s)\n", ev.TS.Format(time.RFC3339))
		}
	}
}

func budgetAmount(dimension string, n int64) string {
	if dimension == "wall" {
		return time.Duration(n).String()
	}
	return strconv.FormatInt(n, 10)
}

func (c *Chat) checkpoint(ev event.Checkpoint) {
	p := ev.Checkpoint
	fmt.Fprintf(c.Out, "\n─── checkpoint: %s %s (soft %s, hard %s)\n", p.Dimension, budgetAmount(p.Dimension, p.Used), budgetAmount(p.Dimension, p.Limit.Soft), budgetAmount(p.Dimension, p.Limit.Hard))
	fmt.Fprintf(c.Out, "  %d tool calls, %d tokens, %d actions, %s elapsed\n", p.State.ToolCalls, p.State.Tokens, p.State.Actions, p.State.Elapsed.Round(time.Second))
	if len(p.State.Files) > 0 {
		fmt.Fprintf(c.Out, "  files: %s\n", strings.Join(p.State.Files, ", "))
	}
	if len(p.State.Commands) > 0 {
		fmt.Fprintf(c.Out, "  commands: %s\n", strings.Join(p.State.Commands, "; "))
	}
	for {
		line, ok := c.lines.read("continue? [y=double soft/N/absolute soft limit] ")
		if !ok {
			line = "n"
		}
		if _, err := budget.ParseChoice(line, p); err != nil {
			fmt.Fprintln(c.Out, err)
			continue
		}
		if err := c.Session.Answer(ev.ID, line); err != nil {
			fmt.Fprintln(c.Out, err)
		}
		return
	}
}
