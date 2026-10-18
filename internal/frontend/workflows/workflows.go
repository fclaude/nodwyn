// Package workflows is what the terminal clients (the TUI's :workflows and
// :outbox, and nodwyn workflows and nodwyn outbox) share of ADR 53: reading
// the daemon's workflows (timetables, which are scheduled tasks, and those that
// watch a source) and the answers they hold, saying them in words, and acting
// on them by a short reference. The daemon says the sentences and statuses;
// this only lays them out, so the console, the TUI and the CLI tell the same
// story.
package workflows

import (
	"context"
	"fmt"
	"strings"

	"github.com/fclaude/nodwyn/internal/frontend/plain"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
)

// Caller is what the daemon connection offers: *rpc.Client.
type Caller interface {
	Call(ctx context.Context, method string, params, result any) error
}

// List reads every workflow.
func List(ctx context.Context, c Caller) ([]rpc.WorkflowView, error) {
	var views []rpc.WorkflowView
	err := c.Call(ctx, "workflow.list", struct{}{}, &views)
	return views, err
}

// Held reads the answers waiting for the person.
func Held(ctx context.Context, c Caller) ([]rpc.OutboxView, error) {
	var items []rpc.OutboxView
	err := c.Call(ctx, "outbox.list", struct{}{}, &items)
	return items, err
}

// Ref is the short name a person types for an id: its last eight characters,
// the random part of the ids the daemon makes.
func Ref(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

// FindWorkflow is the one workflow whose id ends with ref, or says why none is
// meant: no match, or several.
func FindWorkflow(views []rpc.WorkflowView, ref string) (rpc.WorkflowView, error) {
	var found []rpc.WorkflowView
	for _, v := range views {
		if ref != "" && strings.HasSuffix(v.ID, ref) {
			found = append(found, v)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return rpc.WorkflowView{}, fmt.Errorf("no workflow ends with %q: the list shows each one's reference in brackets", ref)
	}
	return rpc.WorkflowView{}, fmt.Errorf("%q ends %d workflows: give more of it", ref, len(found))
}

// FindHeld is the one held answer whose id ends with ref.
func FindHeld(items []rpc.OutboxView, ref string) (rpc.OutboxView, error) {
	var found []rpc.OutboxView
	for _, i := range items {
		if ref != "" && strings.HasSuffix(i.ID, ref) {
			found = append(found, i)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return rpc.OutboxView{}, fmt.Errorf("no held answer ends with %q: the list shows each one's reference in brackets", ref)
	}
	return rpc.OutboxView{}, fmt.Errorf("%q ends %d held answers: give more of it", ref, len(found))
}

// Format lays the workflows out, one block each.
func Format(views []rpc.WorkflowView) string {
	if len(views) == 0 {
		return "No workflows. A timetable starts from Home's Schedule dialog or a task's Schedule…; one that watches pull requests, email or a command is made in the console's Workflows page (Watch for events).\n"
	}
	var b strings.Builder
	for i, v := range views {
		if i > 0 {
			b.WriteString("\n")
		}
		d := v.Definition
		fmt.Fprintf(&b, "%s  [%s]\n", d.Name, Ref(v.ID))
		fmt.Fprintf(&b, "  %s\n", v.Sentence)
		fmt.Fprintf(&b, "  %s\n", v.Status)
		today := "No run today"
		if v.Today.Runs > 0 {
			today = fmt.Sprintf("Today: %d run%s · %s", v.Today.Runs, plural(v.Today.Runs), plain.USD(v.Today.Cost, false))
		}
		if d.DayCost > 0 {
			today += fmt.Sprintf(" of %s a day", plain.USD(d.DayCost, false))
		}
		if v.Running > 0 {
			today += fmt.Sprintf(" · %d running", v.Running)
		}
		fmt.Fprintf(&b, "  %s\n", today)
		if n := len(v.Outbox); n > 0 {
			fmt.Fprintf(&b, "  %d answer%s waiting to be posted: :outbox, or nodwyn outbox\n", n, plural(n))
		}
		if v.LastSkip != nil {
			fmt.Fprintf(&b, "  Note: %s\n", v.LastSkip.Reason)
		}
		for _, f := range v.Recent {
			fmt.Fprintf(&b, "  %s  %s\n", f.When, f.Summary)
		}
	}
	return b.String()
}

// FormatHeld lays the held answers out: the reference to decide by, what it
// answers and the start of the answer.
func FormatHeld(items []rpc.OutboxView) string {
	if len(items) == 0 {
		return "Nothing is waiting to be posted.\n"
	}
	var b strings.Builder
	for i, o := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		what := "Review for #" + o.Item
		verb := "post"
		if o.Kind == "draft" {
			what, verb = "Draft reply", "save the draft"
			if o.Reply != nil {
				what += " to " + o.Reply.To
			}
		}
		fmt.Fprintf(&b, "[%s] %s · %s · %s\n", Ref(o.ID), what, o.Name, o.When)
		fmt.Fprintf(&b, "  %s\n", o.Summary)
		lines := strings.Split(strings.TrimSpace(o.Body), "\n")
		for j, l := range lines {
			if j == 6 {
				fmt.Fprintf(&b, "  > … (%d more lines; the whole answer is in the task %s)\n", len(lines)-6, o.Task)
				break
			}
			fmt.Fprintf(&b, "  > %s\n", l)
		}
		fmt.Fprintf(&b, "  to %s it: post %s; to drop it: discard %s\n", verb, Ref(o.ID), Ref(o.ID))
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Act pauses, resumes or checks a workflow, and says what was done.
func Act(ctx context.Context, c Caller, view rpc.WorkflowView, action string) (string, error) {
	switch action {
	case "pause", "resume":
		var v rpc.WorkflowView
		if err := c.Call(ctx, "workflow.pause", map[string]any{"id": view.ID, "paused": action == "pause"}, &v); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s: %s", view.Definition.Name, v.Status), nil
	case "check":
		var v rpc.WorkflowView
		if err := c.Call(ctx, "workflow.check", map[string]any{"id": view.ID}, &v); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s: %s", view.Definition.Name, v.Status), nil
	}
	return "", fmt.Errorf("a workflow is paused, resumed or checked, not %q", action)
}

// Decide posts or discards a held answer, as the person's decision, and says
// what was done. The words are the run's: changing them is the console's.
func Decide(ctx context.Context, c Caller, item rpc.OutboxView, decision string) (string, error) {
	if err := c.Call(ctx, "outbox.decide", map[string]any{"workflow": item.Workflow, "id": item.ID, "decision": decision}, nil); err != nil {
		return "", err
	}
	if decision == "discard" {
		return "Discarded. Nothing was " + map[bool]string{true: "saved", false: "posted"}[item.Kind == "draft"] + ".", nil
	}
	if item.Kind == "draft" {
		return "Saved as a draft in Gmail. Nothing was sent.", nil
	}
	return "Posted.", nil
}

// Listing is everything the console's Workflows page and Needs you hold of
// workflows: the timetables (scheduled tasks), the workflows that watch a
// source, and the answers held for the person.
type Listing struct {
	Timetables []rpc.SessionInfo
	Watching   []rpc.WorkflowView
	Held       []rpc.OutboxView
}

// Read reads a Listing from the daemon.
func Read(ctx context.Context, c Caller) (Listing, error) {
	var l Listing
	var sessions []rpc.SessionInfo
	if err := c.Call(ctx, "session.list", struct{}{}, &sessions); err != nil {
		return l, err
	}
	for _, s := range sessions {
		if s.Schedule != nil && s.Schedule.Definition != nil && !s.Archived {
			l.Timetables = append(l.Timetables, s)
		}
	}
	var err error
	if l.Watching, err = List(ctx, c); err != nil {
		return l, err
	}
	l.Held, err = Held(ctx, c)
	return l, err
}

// Workflows says every workflow: timetables first, then those that watch a
// source.
func (l Listing) Workflows() string {
	if len(l.Timetables) == 0 {
		return Format(l.Watching)
	}
	var b strings.Builder
	for i, s := range l.Timetables {
		if i > 0 {
			b.WriteString("\n")
		}
		name := s.Title
		if name == "" {
			name = s.FirstMessage
		}
		sc := s.Schedule
		fmt.Fprintf(&b, "%s  [%s]  a timetable: the runs go into this task\n", name, Ref(s.ID))
		fmt.Fprintf(&b, "  %s\n", sc.Sentence)
		switch {
		case sc.Definition.Paused:
			b.WriteString("  Paused\n")
		case sc.Held != "":
			fmt.Fprintf(&b, "  %s\n", sc.Held)
		case len(sc.NextWords) > 0:
			fmt.Fprintf(&b, "  Next: %s\n", sc.NextWords[0])
		default:
			b.WriteString("  No automatic time pending\n")
		}
		today := "No run today"
		if sc.Today.Runs > 0 {
			today = fmt.Sprintf("Today: %d run%s · %s", sc.Today.Runs, plural(sc.Today.Runs), plain.USD(sc.Today.Cost, false))
		}
		if sc.Definition.DayCost > 0 {
			today += fmt.Sprintf(" of %s a day", plain.USD(sc.Definition.DayCost, false))
		}
		fmt.Fprintf(&b, "  %s", today)
		if len(sc.History) > 0 {
			fmt.Fprintf(&b, " · Last: %s", sc.History[0].What)
		}
		b.WriteString("\n")
	}
	if len(l.Watching) > 0 {
		b.WriteString("\n" + Format(l.Watching))
	}
	return b.String()
}

// HeldAnswers says the answers held to be posted.
func (l Listing) HeldAnswers() string { return FormatHeld(l.Held) }

// Act pauses, resumes or checks a workflow that watches a source, or pauses,
// resumes or runs now a timetable, by the reference the list shows. It says
// what was done.
func (l Listing) Act(ctx context.Context, c Caller, ref, action string) (string, error) {
	var tables []rpc.SessionInfo
	for _, s := range l.Timetables {
		if ref != "" && strings.HasSuffix(s.ID, ref) {
			tables = append(tables, s)
		}
	}
	watching, watchErr := FindWorkflow(l.Watching, ref)
	switch {
	case len(tables) == 1 && watchErr != nil:
		s := tables[0]
		name := s.Title
		if name == "" {
			name = s.FirstMessage
		}
		switch action {
		case "pause", "resume":
			if err := c.Call(ctx, "schedule.pause", map[string]any{"id": s.ID, "paused": action == "pause"}, nil); err != nil {
				return "", err
			}
			return name + ": " + map[string]string{"pause": "paused", "resume": "resumed"}[action] + ".", nil
		case "run":
			if err := c.Call(ctx, "schedule.run", map[string]any{"id": s.ID}, nil); err != nil {
				return "", err
			}
			return name + ": a run started.", nil
		}
		return "", fmt.Errorf("a timetable is paused, resumed or run now, not %q", action)
	case len(tables) > 1:
		return "", fmt.Errorf("%q ends %d timetables: give more of it", ref, len(tables))
	case watchErr != nil:
		return "", watchErr
	}
	if action == "run" {
		action = "check"
	}
	return Act(ctx, c, watching, action)
}

// Decide posts or discards the held answer a reference names.
func (l Listing) Decide(ctx context.Context, c Caller, ref, decision string) (string, error) {
	item, err := FindHeld(l.Held, ref)
	if err != nil {
		return "", err
	}
	return Decide(ctx, c, item, decision)
}
