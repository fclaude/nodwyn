package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nibb/internal/frontend/plain"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

const frictionSource = `(fs-read "docs/friction.md")`

type inspector struct {
	command, title, content string
	token                   int
	loading                 bool
	// Friction reads are asynchronous evaluations. Only their matching request
	// arms capture, and leaving the view prevents a late result reopening it.
	reading, started bool
}
type inspectResult struct {
	token, generation int
	content           string
	err               error
}

func isInspectorCommand(s string) bool {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case ":log", ":bindings", ":replay", ":friction":
		return true
	}
	return false
}
func (m *model) inspect(command string) tea.Cmd {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	kind := fields[0]
	count, filter, err := logFilter(fields)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	if kind == ":friction" && m.busy {
		m.status = "The friction read needs an idle session; Ctrl+C aborts busy work."
		return nil
	}
	m.inspectionToken++
	p := &inspector{command: command, title: strings.TrimPrefix(kind, ":"), content: "Loading…", token: m.inspectionToken, loading: true}
	m.inspector = p
	m.status = ""
	m.input.Reset()
	m.refresh(false)
	m.viewport.GotoTop()
	if kind == ":friction" {
		p.reading = true
		m.busy = true
		b, id := m.backend, m.id
		return m.action("friction", "", func(ctx context.Context) error { return b.Send(ctx, id, frictionSource, true) })
	}
	b, id, gen, thinking := m.backend, m.id, m.generation, m.thinking
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer cancel()
		var content string
		var err error
		if kind == ":bindings" {
			var policy policyView
			policy, err = b.Policy(ctx, id)
			if err == nil {
				content = formatPolicy(policy)
			}
		} else {
			var events []log.Event
			events, err = b.History(ctx, id)
			if err == nil {
				if kind == ":log" {
					content = formatLog(events, count, filter)
				} else {
					var out bytes.Buffer
					plain.Replay(events, &out, thinking)
					content = out.String()
				}
				if len(events) > 0 {
					content = fmt.Sprintf("Snapshot through seq %d · refresh with r\n\n", events[len(events)-1].Seq) + content
				}
			}
		}
		return inspectResult{token: p.token, generation: gen, content: content, err: err}
	}
}
func logFilter(fields []string) (int, string, error) {
	count, filter := 20, ""
	if fields[0] != ":log" {
		if len(fields) > 1 {
			return 0, "", fmt.Errorf("%s takes no arguments", fields[0])
		}
		return count, filter, nil
	}
	args := fields[1:]
	if len(args) > 0 {
		if n, err := strconv.Atoi(args[0]); err == nil {
			if n < 1 || n > 1000 {
				return 0, "", fmt.Errorf(":log count must be between 1 and 1000")
			}
			count = n
			args = args[1:]
		}
	}
	if len(args) > 1 {
		return 0, "", fmt.Errorf("usage: :log [1–1000] [kind prefix]")
	}
	if len(args) == 1 {
		filter = args[0]
	}
	return count, filter, nil
}
func formatLog(events []log.Event, count int, filter string) string {
	var matches []log.Event
	for _, e := range events {
		if strings.HasPrefix(string(e.Kind), filter) {
			matches = append(matches, e)
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%d matching records · showing the last %d\n", len(matches), min(count, len(matches)))
	for _, e := range matches[max(0, len(matches)-count):] {
		fmt.Fprintf(&out, "\n#%d %s · %s\n", e.Seq, e.Kind, e.TS.Format(time.RFC3339))
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, e.Payload, "", "  "); err != nil {
			out.WriteString("[invalid payload]")
		} else {
			out.Write(pretty.Bytes())
		}
		out.WriteByte('\n')
	}
	return out.String()
}
func formatPolicy(p policyView) string {
	var out strings.Builder
	out.WriteString("Loaded session bindings · metadata only\nChanges to bindings.toml apply when a session is reopened.\n\n")
	if len(p.Bindings) == 0 {
		out.WriteString("No bindings loaded. Secret-bearing egress is unbound.\n")
	}
	for _, b := range p.Bindings {
		fmt.Fprintf(&out, "%s → %s\n  methods: %s\n  positions: %s\n\n", b.Class, b.Dest, strings.Join(b.Methods, ", "), strings.Join(b.Positions, ", "))
	}
	fmt.Fprintf(&out, "\nCurrent command approvals: %s\nCurrent write approvals: %s\nOrdinary writes approved this turn: %t\n", strings.Join(p.Commands, ", "), strings.Join(p.Writes, ", "), p.TurnWrites)
	out.WriteString("\nThis view cannot create or change permissions.")
	return out.String()
}
func (m *model) frictionRequest(source string) {
	p := m.inspector
	if p == nil || !p.reading {
		return
	}
	if source == frictionSource {
		p.started = true
	} else {
		p.reading = false
		p.loading = false
		p.content = "Another evaluation started; refresh after the session is idle."
	}
}
func (m *model) frictionResult(result log.EvalResult) {
	p := m.inspector
	if p == nil || !p.reading || !p.started {
		return
	}
	p.reading = false
	p.loading = false
	if result.Error != "" {
		p.content = "Friction read failed: " + safe(result.Error)
		return
	}
	value, err := scheme.ReadOne(result.Value)
	if err != nil || value.Kind != scheme.KString {
		p.content = "Friction read returned no text."
		return
	}
	p.content = "docs/friction.md · guarded read\n\n" + safe(value.S)
	m.refresh(false)
	m.viewport.GotoTop()
}

const helpText = `Commands

:log [n] [kind prefix]  Last matching durable records (default 20, max 1000).
                       Example: :log 50 guard.
:bindings              Loaded bindings and current approvals.
:replay                Read-only transcript from the durable log.
:friction              Guarded read of docs/friction.md; needs an idle session.
:eval EXPR             User-origin Scheme evaluation.
:thinking              Show or hide thinking.
:help                  This view.

Inspectors are fixed snapshots. r refreshes; Esc returns to conversation.
Pending approvals always take priority over an inspector.
PgUp/PgDn scroll; Ctrl+End jumps to the bottom.
Ctrl+C aborts busy work, or quits when idle.
Ctrl+Q quits; a daemon session keeps running after detach.
Ctrl+R reconnects in daemon mode, without retrying operations.`
