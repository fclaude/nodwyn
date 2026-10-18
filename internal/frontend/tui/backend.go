// Package tui presents sessions without owning interpreter or policy state.
package tui

import (
	"context"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/frontend/workflows"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

type snapshot struct {
	ID, Repo string
	// Project is the folder the session's notes, repository index and
	// earlier tasks belong to: the repository of a worktree's session.
	Project string
	// Branch is the session's own branch when it works in a git worktree
	// (ADR 34), which the title shows.
	Branch  string
	Busy    bool
	History []log.Event
	Pending []rpc.ClientEvent
}

// backend is the small set of operations these views need, each a call to
// the daemon, which owns sessions, prompts and authorization (ADR 30,
// amended 2026-10-17). A connection is retried only when the person asks;
// a submitted operation never is.
type backend interface {
	List(context.Context) ([]rpc.SessionInfo, error)
	// Attach opens a session: the stored one id names, or with id "" a new
	// one in repo. OpenWorktree opens a new one in a git worktree of repo, on
	// a branch of its own (ADR 34).
	Attach(context.Context, string, string) (snapshot, error)
	OpenWorktree(context.Context, string) (snapshot, error)
	Next(context.Context) (rpc.ClientEvent, error)
	// Models says which model the conversation talks to and which it may be
	// moved to; SetModel moves it, by a choice's name and an effort (ADR 41).
	Models(context.Context, string) (modelView, error)
	SetModel(context.Context, string, string, string) (core.ModelState, error)
	// Judges says which judge the task asks and which it may ask instead;
	// SetJudge moves it, by a judge's name or "" for none (ADR 47).
	Judges(context.Context, string) (judgeView, error)
	SetJudge(context.Context, string, string) (core.JudgeState, error)
	// AttachFile adds a file the person named to the conversation (ADR 22).
	// An image's confirm arrives as a prompt while it waits.
	AttachFile(context.Context, string, string) (log.Attach, error)
	Send(context.Context, string, string, bool) error
	Answer(context.Context, string, string, string) error
	Grant(context.Context, string, string, string) error
	Bind(context.Context, string, string) error
	Secret(context.Context, string, string, []byte) error
	Abort(context.Context, string) error
	// CloseSession closes the session in the daemon, as the console's
	// Close does: its log is released and a worktree with nothing unlanded
	// is removed. Quitting only detaches.
	CloseSession(context.Context, string) error
	History(context.Context, string) ([]log.Event, error)
	Policy(context.Context, string) (policyView, error)
	Tools(context.Context, string) ([]llm.Tool, error)
	Budgets(context.Context, string) (budget.Limits, error)
	// Retrieval says what a project keeps for the model's searches (ADR
	// 50): its repository index, and whether its earlier tasks are
	// searched, which SetEarlierTasks changes; ForgetIndex deletes the index.
	Retrieval(context.Context, string) (retrievalView, error)
	SetEarlierTasks(context.Context, string, bool) error
	ForgetIndex(context.Context, string) error
	// Workflows reads the daemon's workflows and the answers they hold for
	// the person (ADR 53). WorkflowAct pauses, resumes or checks the one a
	// reference names, and Decide posts or discards a held answer; each says
	// what was done. The words come from the daemon, laid out as the CLI does.
	Workflows(context.Context) (workflows.Listing, error)
	WorkflowAct(context.Context, string, string) (string, error)
	Decide(context.Context, string, string) (string, error)
	Close()
}
