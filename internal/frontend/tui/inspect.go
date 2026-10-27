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
	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/frontend/plain"
	"github.com/fclaude/nodwyn/internal/frontend/workflows"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
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
	case ":log", ":bindings", ":replay", ":friction", ":tools", ":allow", ":plan", ":budget":
		return true
	case ":model", ":judge", ":index", ":earlier", ":workflows", ":outbox":
		// With a name or an action it is a choice, not a view.
		return len(fields) == 1
	}
	return false
}
func (m *model) inspect(command string) tea.Cmd {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	kind := fields[0]
	count, filter, err := inspectorArgs(fields)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	if kind == ":friction" && m.busy {
		m.status = "The friction read needs an idle task; Ctrl+C aborts busy work."
		return nil
	}
	m.inspectionToken++
	p := &inspector{command: command, title: strings.TrimPrefix(kind, ":"), content: "Loading…", token: m.inspectionToken, loading: true}
	m.inspector = p
	m.status = ""
	m.composer.Reset()
	m.refresh(false)
	m.viewport.GotoTop()
	if kind == ":friction" {
		p.reading = true
		m.busy = true
		b, id := m.backend, m.id
		return m.action("friction", "", func(ctx context.Context) error { return b.Send(ctx, id, frictionSource, true) })
	}
	b, id, gen, thinking := m.backend, m.id, m.generation, m.thinking
	project := m.project
	if project == "" {
		project = m.repo
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer cancel()
		var content string
		var err error
		if kind == ":tools" {
			var catalog []llm.Tool
			catalog, err = b.Tools(ctx, id)
			if err == nil {
				content, err = formatTools(catalog, filter)
			}
		} else if kind == ":model" {
			var view modelView
			view, err = b.Models(ctx, id)
			if err == nil {
				content = formatModels(view)
			}
		} else if kind == ":judge" {
			var view judgeView
			view, err = b.Judges(ctx, id)
			if err == nil {
				content = formatJudges(view)
			}
		} else if kind == ":workflows" || kind == ":outbox" {
			var listing workflows.Listing
			listing, err = b.Workflows(ctx)
			if err == nil && kind == ":workflows" {
				content = listing.Workflows()
			} else if err == nil {
				content = listing.HeldAnswers()
			}
		} else if kind == ":index" || kind == ":earlier" {
			var view retrievalView
			view, err = b.Retrieval(ctx, project)
			if err == nil {
				content = formatRetrieval(view)
			}
		} else if kind == ":bindings" || kind == ":allow" {
			var policy policyView
			policy, err = b.Policy(ctx, id)
			if err == nil {
				content = formatPolicy(policy)
			}
		} else {
			var events []log.Event
			events, err = b.History(ctx, id)
			if err == nil {
				switch kind {
				case ":log":
					content = formatLog(events, count, filter)
				case ":plan":
					content, err = formatPlan(events)
				case ":budget":
					var limits budget.Limits
					limits, err = b.Budgets(ctx, id)
					if err == nil {
						content, err = formatBudgets(limits, events)
					}
				default:
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
func inspectorArgs(fields []string) (int, string, error) {
	if fields[0] == ":tools" {
		if len(fields) > 2 {
			return 0, "", fmt.Errorf("usage: :tools [exact name]")
		}
		if len(fields) == 2 {
			return 0, fields[1], nil
		}
		return 0, "", nil
	}
	return logFilter(fields)
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
	out.WriteString("Loaded session bindings · metadata only\nLast validated snapshot. Changes to bindings.toml reload before egress; invalid files deny egress.\n\n")
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
:plan                  Latest user message’s recorded planning outcome.
:budget                The task’s limits and latest turn’s recorded checkpoints.
:tools [name]          Tools offered to the model; exact name shows input schema.
:bindings, :allow      Loaded bindings and current approvals.
:model                 The model this conversation talks to, and the choices.
:model NAME [EFFORT]   Move the conversation to that model from its next turn.
:judge                 The judge this task asks, and the judges set up.
:judge NAME|none       Ask that judge, or none, from the next question.
:index                 The repository index the model's searches read.
:index forget          Delete it; the next search builds it again.
:earlier               Whether the model may search this folder's other tasks.
:earlier on|off        Let it, or stop it, from the model's next call.
:workflows             The daemon's workflows: when each runs, what it did lately.
:workflows pause|resume|check|run REF
                       Act on the workflow or timetable whose reference [in
                       brackets] is REF (run now is for a timetable).
:outbox                Answers workflows hold for you to post or save as drafts.
:outbox post|discard REF
                       Decide one by its reference; the daemon posts, never a run.
:attach PATH           Add a file to the conversation; an image asks first.
                       ~/ is home; a relative path is from where nodwyn started.
:replay                Read-only transcript from the durable log.
:friction              Guarded read of docs/friction.md; needs an idle session.
:eval EXPR             User-origin Scheme evaluation.
:thinking              Show or hide thinking.
:details               Show or hide tool arguments and results inline.
:help, :h              This view.
:q, :quit, :exit       Quit, as Ctrl+Q does.
:close                 Close the task and quit: its log is released, and a
                       worktree with nothing unlanded is removed.

Inspectors are fixed snapshots. r refreshes; Esc returns to conversation.
Pending approvals always take priority over an inspector.
Enter sends a message. Alt+Enter or Ctrl+J inserts a newline.
Bracketed paste preserves line breaks and never sends.
PgUp/PgDn scroll; Ctrl+End jumps to the bottom.
Ctrl+C aborts busy work, or quits when idle.
Ctrl+Q detaches: the task and its turn go on in the daemon, for every
client and the scheduler. When this process started the daemon (none was
running), quitting ends its tasks with it.
Ctrl+R reconnects to the daemon, without retrying operations.`
