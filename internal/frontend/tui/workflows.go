package tui

import (
	"context"

	"github.com/fclaude/nodwyn/internal/frontend/workflows"
)

// Workflows reads what :workflows and :outbox show: the daemon's own words,
// laid out by the package the command line uses as well.
func (r *remote) Workflows(ctx context.Context) (workflows.Listing, error) {
	return workflows.Read(ctx, r.client)
}

// WorkflowAct pauses, resumes, checks or runs now the workflow or timetable a
// reference names, read again so that the reference means what the list says.
func (r *remote) WorkflowAct(ctx context.Context, ref, action string) (string, error) {
	l, err := workflows.Read(ctx, r.client)
	if err != nil {
		return "", err
	}
	return l.Act(ctx, r.client, ref, action)
}

// Decide posts or discards the held answer a reference names. The words are
// the run's: a terminal changes none (the console edits them).
func (r *remote) Decide(ctx context.Context, ref, decision string) (string, error) {
	l, err := workflows.Read(ctx, r.client)
	if err != nil {
		return "", err
	}
	return l.Decide(ctx, r.client, ref, decision)
}
