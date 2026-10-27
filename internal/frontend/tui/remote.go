package tui

import (
	"context"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/log"
)

type remote struct {
	client *rpc.Client
	// socket is the daemon's, for a call that needs a connection of its own.
	socket string
}

func (r *remote) List(ctx context.Context) (out []rpc.SessionInfo, err error) {
	err = r.client.Call(ctx, "session.list", struct{}{}, &out)
	return
}
func (r *remote) Attach(ctx context.Context, repo, id string) (snapshot, error) {
	var info rpc.SessionDetails
	method, params := "session.resume", map[string]any{"id": id}
	if id == "" {
		method, params = "session.open", map[string]any{"repo": repo}
	}
	if err := r.client.Call(ctx, method, params, &info); err != nil {
		return snapshot{}, err
	}
	return r.subscribe(ctx, info)
}

// subscribe follows an open session and reads its history up to the live
// events, page by page.
func (r *remote) subscribe(ctx context.Context, info rpc.SessionDetails) (snapshot, error) {
	var sub struct {
		Page    core.LogPage      `json:"page"`
		Busy    bool              `json:"busy"`
		Pending []rpc.ClientEvent `json:"pending"`
	}
	if err := r.client.Call(ctx, "session.subscribe", map[string]any{"id": info.ID, "limit": 100}, &sub); err != nil {
		return snapshot{}, err
	}
	result := snapshot{ID: info.ID, Repo: info.Repo, Project: info.Repo, Busy: sub.Busy, History: sub.Page.Events, Pending: sub.Pending}
	if info.Worktree != nil {
		result.Branch = info.Worktree.Branch
		result.Project = config.CanonicalPath(info.Worktree.Repository)
	}
	// Each page is decoded into a page of its own: decoding into the last
	// one would reuse its events' array, which the history holds, and
	// write the second page over the first.
	for page := sub.Page; page.More; {
		var next core.LogPage
		if err := r.client.Call(ctx, "log.read", map[string]any{"id": info.ID, "after": page.Next, "through": page.Through, "limit": 100}, &next); err != nil {
			return snapshot{}, err
		}
		result.History = append(result.History, next.Events...)
		page = next
	}
	return result, nil
}
func (r *remote) Next(ctx context.Context) (rpc.ClientEvent, error) { return r.client.Next(ctx) }
func (r *remote) Send(ctx context.Context, id, text string, eval bool) error {
	method, key := "session.send", "text"
	if eval {
		method, key = "session.eval", "source"
	}
	return r.client.Call(ctx, method, map[string]any{"id": id, key: text}, nil)
}
func (r *remote) SendWhileBusy(ctx context.Context, id, text, mode string) error {
	return r.client.Call(ctx, "session.send", map[string]any{"id": id, "text": text, "mode": mode}, nil)
}
func (r *remote) Answer(ctx context.Context, id, prompt, reply string) error {
	return r.client.Call(ctx, "session.answer", map[string]any{"id": id, "prompt_id": prompt, "reply": reply}, nil)
}
func (r *remote) Grant(ctx context.Context, id, prompt, scope string) error {
	return r.client.Call(ctx, "session.approval.create", map[string]any{"id": id, "prompt_id": prompt, "scope": scope}, nil)
}
func (r *remote) Secret(ctx context.Context, id, prompt string, value []byte) error {
	return r.client.Call(ctx, "session.secret", map[string]any{"id": id, "prompt_id": prompt, "secret": string(value)}, nil)
}
func (r *remote) Abort(ctx context.Context, id string) error {
	return r.client.Call(ctx, "session.abort", map[string]any{"id": id}, nil)
}
func (r *remote) CloseSession(ctx context.Context, id string) error {
	return r.client.Call(ctx, "session.close", map[string]any{"id": id}, nil)
}
func (r *remote) Close() { r.client.Close() }

// OpenWorktree is Attach's new session, made in a git worktree of repo.
func (r *remote) OpenWorktree(ctx context.Context, repo string) (snapshot, error) {
	var info rpc.SessionDetails
	if err := r.client.Call(ctx, "worktree.open", map[string]any{"repo": repo}, &info); err != nil {
		return snapshot{}, err
	}
	return r.subscribe(ctx, info)
}

// AttachFile asks on a connection of its own: the daemon runs one
// connection's calls in order, so an image's confirm could not be answered
// on this one while session.attach waits for it.
func (r *remote) AttachFile(ctx context.Context, id, path string) (log.Attach, error) {
	c, err := rpc.Dial(ctx, r.socket)
	if err != nil {
		return log.Attach{}, err
	}
	defer c.Close()
	var receipt log.Attach
	err = c.Call(ctx, "session.attach", map[string]any{"id": id, "path": path}, &receipt)
	return receipt, err
}

func (r *remote) Bind(ctx context.Context, id, prompt string) error {
	return r.client.Call(ctx, "session.binding.create", map[string]any{"id": id, "prompt_id": prompt}, nil)
}
