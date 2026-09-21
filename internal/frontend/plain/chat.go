package plain

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
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
	// Interrupt receives a value per Ctrl-C; nil disables.
	Interrupt <-chan struct{}

	lines  *bufio.Scanner
	recent []event.Event
}

// Run drives the session until :q or EOF.
func (c *Chat) Run(ctx context.Context) error {
	c.lines = bufio.NewScanner(c.In)
	c.lines.Buffer(make([]byte, 0, 64<<10), 16<<20)
	c.banner()
	// Drain whatever the session emitted before we attached (session.start).
	c.drainIdle()
	for {
		fmt.Fprint(c.Out, "\nyou> ")
		line, ok := c.readLine()
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
			fmt.Fprintln(c.Out, "error:", err)
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
	fmt.Fprintf(c.Out, "nibb session %s\nrepo  %s\ntools %s\n:h for commands\n", s.ID(), s.Repo(), strings.Join(names, " "))
}

func (c *Chat) readLine() (string, bool) {
	if !c.lines.Scan() {
		return "", false
	}
	return c.lines.Text(), true
}

// drainIdle consumes buffered events without blocking.
func (c *Chat) drainIdle() {
	for {
		select {
		case ev, ok := <-c.Session.Events():
			if !ok {
				return
			}
			c.show(ev)
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

var lastWasStream bool

func (c *Chat) show(ev event.Event) {
	if ev.Seq != 0 {
		c.recent = append(c.recent, ev)
		if len(c.recent) > 200 {
			c.recent = c.recent[len(c.recent)-200:]
		}
	}
	endStream := func() {
		if lastWasStream {
			fmt.Fprintln(c.Out)
			lastWasStream = false
		}
	}
	switch ev.Kind {
	case event.StreamThinking:
		if c.ShowThinking {
			fmt.Fprint(c.Out, dim(ev.Payload.(event.Text).Text))
			lastWasStream = true
		}
	case event.StreamText:
		fmt.Fprint(c.Out, ev.Payload.(event.Text).Text)
		lastWasStream = true
	case event.Output:
		fmt.Fprint(c.Out, ev.Payload.(event.Text).Text)
		lastWasStream = true
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
	case event.PromptConfirm:
		endStream()
		c.confirm(ev.Payload.(event.Confirm))
	case event.Error:
		endStream()
		fmt.Fprintf(c.Out, "error: %s\n", ev.Payload.(event.ErrorInfo).Err)
	case event.Status:
		if ev.Payload.(event.StatusInfo).State == "idle" {
			endStream()
		}
	case string(log.KindNote):
		endStream()
		fmt.Fprintf(c.Out, "note: %s\n", ev.Payload.(log.Note).Text)
	}
}

func (c *Chat) confirm(p event.Confirm) {
	fmt.Fprintf(c.Out, "\n─── confirm: %s %s\n", p.Prompt.Prim, p.Prompt.Args)
	for _, r := range p.Prompt.Reasons {
		fmt.Fprintf(c.Out, "  • %s\n", r)
	}
	if p.Prompt.Detail != "" {
		fmt.Fprintln(c.Out, indent(p.Prompt.Detail, "  "))
	}
	switch {
	case len(p.Prompt.Argv) > 0:
		fmt.Fprint(c.Out, "allow? [y/N/a=always this session] ")
	case p.Prompt.Path != "" && !guard.IsProtected(p.Prompt.Path):
		fmt.Fprintf(c.Out, "allow? [y/N/a=always under %s/t=all writes this turn] ", guard.DirPrefix(p.Prompt.Path))
	default:
		fmt.Fprint(c.Out, "allow? [y/N] ")
	}
	line, ok := c.readLine()
	if !ok {
		line = "n"
	}
	_ = c.Session.Answer(p.ID, line)
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
		v, err := c.Session.Eval(ctx, src)
		c.drainIdle()
		if err != nil {
			fmt.Fprintln(c.Out, "error:", err)
		} else if v.Kind != scheme.KVoid {
			fmt.Fprintln(c.Out, scheme.Write(v))
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
		case log.KindUserMsg:
			var p log.UserMsg
			_ = ev.Decode(&p)
			fmt.Fprintf(w, "\nyou> %s\n", p.Text)
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
		case log.KindSessionEnd:
			fmt.Fprintf(w, "\n# end (%s)\n", ev.TS.Format(time.RFC3339))
		}
	}
}
