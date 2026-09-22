package tui

import (
	"context"

	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/frontend/rpc"
)

type remote struct{ client *rpc.Client }

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
	var sub struct {
		Page    core.LogPage      `json:"page"`
		Busy    bool              `json:"busy"`
		Pending []rpc.ClientEvent `json:"pending"`
	}
	if err := r.client.Call(ctx, "session.subscribe", map[string]any{"id": info.ID, "limit": 100}, &sub); err != nil {
		return snapshot{}, err
	}
	result := snapshot{ID: info.ID, Repo: info.Repo, Busy: sub.Busy, History: sub.Page.Events, Pending: sub.Pending}
	page := sub.Page
	for page.More {
		if err := r.client.Call(ctx, "log.read", map[string]any{"id": info.ID, "after": page.Next, "through": page.Through, "limit": 100}, &page); err != nil {
			return snapshot{}, err
		}
		result.History = append(result.History, page.Events...)
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
func (r *remote) Close() { r.client.Close() }
