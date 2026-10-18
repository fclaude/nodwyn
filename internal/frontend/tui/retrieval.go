package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
)

// retrievalView is what :index and :earlier show of the session's project
// (ADR 50): what its repository index holds, and whether the model may
// search its earlier tasks.
type retrievalView struct {
	Project      string
	Index        core.IndexInfo
	EarlierTasks bool
}

// registered is a workspace as workspace.list gives it.
type registered struct {
	Name         string         `json:"name"`
	Path         string         `json:"path"`
	Mounts       []config.Mount `json:"mounts"`
	EarlierTasks bool           `json:"earlier_tasks"`
}

// named is the daemon's entry for the folder project, if it has one.
func (r *remote) named(ctx context.Context, project string) (registered, bool, error) {
	var list []registered
	if err := r.client.Call(ctx, "workspace.list", struct{}{}, &list); err != nil {
		return registered{}, false, err
	}
	for _, w := range list {
		if config.CanonicalPath(w.Path) == project {
			return w, true, nil
		}
	}
	return registered{}, false, nil
}

func (r *remote) Retrieval(ctx context.Context, project string) (retrievalView, error) {
	view := retrievalView{Project: project}
	if err := r.client.Call(ctx, "index.status", map[string]any{"repo": project}, &view.Index); err != nil {
		return retrievalView{}, err
	}
	w, _, err := r.named(ctx, project)
	view.EarlierTasks = w.EarlierTasks
	return view, err
}

// SetEarlierTasks turns searching the project's earlier tasks on or off,
// as the console's Workspaces page does: kept with the folder's name in
// workspaces.toml on this computer (ADR 50 §9).
func (r *remote) SetEarlierTasks(ctx context.Context, project string, on bool) error {
	w, ok, err := r.named(ctx, project)
	if err != nil {
		return err
	}
	if !ok {
		w = registered{Name: filepath.Base(project)}
	}
	if w.Mounts == nil {
		w.Mounts = []config.Mount{}
	}
	// workspace.save replaces the entry: its name and mounts go back as
	// they were.
	return r.client.Call(ctx, "workspace.save", map[string]any{"path": project, "name": w.Name, "mounts": w.Mounts, "earlier_tasks": on}, nil)
}

// ForgetIndex deletes the project's repository index; the next search
// builds it again.
func (r *remote) ForgetIndex(ctx context.Context, project string) error {
	return r.client.Call(ctx, "index.forget", map[string]any{"repo": project}, nil)
}

// formatRetrieval is the :index and :earlier view.
func formatRetrieval(v retrievalView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Retrieval for %s (ADR 50)\n\n", v.Project)
	if v.Index.Bytes == 0 {
		b.WriteString("Repository index: none yet; the model's first search of the repository builds it.\n")
	} else {
		fmt.Fprintf(&b, "Repository index: %d files, %s on disk, updated %s", v.Index.Files, byteSize(int(v.Index.Bytes)), updated(v.Index.Updated))
		if v.Index.Trees > 1 {
			fmt.Fprintf(&b, ", for %d working trees", v.Index.Trees)
		}
		b.WriteString(".\n  :index forget deletes it; the next search builds it again.\n")
	}
	if v.EarlierTasks {
		b.WriteString("\nEarlier tasks: the model may search this folder's other tasks.\n  :earlier off stops it.\n")
	} else {
		b.WriteString("\nEarlier tasks: not searched; each task sees only its own conversation.\n  :earlier on lets the model search this folder's other tasks: what was asked\n  in them, its replies and what its tools returned, from the logs on this computer.\n")
	}
	return b.String()
}

// updated says when an index last changed, from since's short age.
func updated(t time.Time) string {
	age := since(t)
	switch {
	case age == "":
		return "at an unknown time"
	case age == "now":
		return "just now"
	case strings.Contains(age, " "):
		return "on " + age
	}
	return age + " ago"
}
