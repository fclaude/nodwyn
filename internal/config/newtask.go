package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// NewTaskChoices is what the console chose last for a new task, which the
// next one starts with: the host it runs on, and for each host the model,
// effort, judge, helpers, limits and worktree picked there. A browser keeps
// what it stores for one origin, so the app's window, another browser or a
// console on another port would start with none; every console reaches the
// daemon's config directory (ADR 41, amended 2026-10-27).
type NewTaskChoices struct {
	// RunsOn is the linked host a new task starts on, "" for this computer.
	RunsOn string           `toml:"runs_on" json:"runs_on"`
	Hosts  []NewTaskForHost `toml:"host" json:"hosts"`
}

// NewTaskForHost is what was chosen for a new task on one host. An empty
// field is that host's default: Settings' model and effort, its judge, no
// helpers, the limits on, and a worktree for a git workspace.
type NewTaskForHost struct {
	Host   string `toml:"host" json:"host"`
	Model  string `toml:"model,omitempty" json:"model,omitempty"`
	Effort string `toml:"effort,omitempty" json:"effort,omitempty"`
	// Judge is nil for the default judge of Settings and "" for no judge,
	// a choice of its own.
	Judge    *string          `toml:"judge,omitempty" json:"judge,omitempty"`
	Helpers  []string         `toml:"helpers,omitempty" json:"helpers,omitempty"`
	NoLimits bool             `toml:"no_limits,omitempty" json:"no_limits,omitempty"`
	Worktree []WorktreeChoice `toml:"worktree,omitempty" json:"worktree,omitempty"`
}

// WorktreeChoice is whether a new task in Folder starts in a worktree of it.
type WorktreeChoice struct {
	Folder string `toml:"folder" json:"folder"`
	On     bool   `toml:"on" json:"on"`
}

// The file only preselects, but a console writes it, so it is kept small:
// a page cannot grow the config directory without bound.
const (
	maxNewTaskHosts     = 64
	maxNewTaskHelpers   = 64
	maxNewTaskWorktrees = 256
	maxNewTaskText      = 512
)

// NewTaskFile is where the choices for the next task are kept. Only the
// daemon writes it, on a newtask.last from a console on this computer or a
// device signed in with the person's passkey, and the person may edit it by
// hand; the config directory is private to the guard, so neither a model nor
// a repository can. It names choices, never a policy: each is checked again
// when a task opens with it.
func NewTaskFile(p Paths) string { return filepath.Join(p.ConfigDir, "new-task.toml") }

// LoadNewTask reads the choices for the next task; missing is none.
func LoadNewTask(path string) (NewTaskChoices, error) {
	var c NewTaskChoices
	_, err := toml.DecodeFile(path, &c)
	if errors.Is(err, os.ErrNotExist) {
		return NewTaskChoices{Hosts: []NewTaskForHost{}}, nil
	}
	if err != nil {
		return NewTaskChoices{}, fmt.Errorf("new task: %w", err)
	}
	if c.Hosts == nil {
		c.Hosts = []NewTaskForHost{}
	}
	return c, nil
}

// CheckNewTask says what is wrong with choices before they are kept, or nil.
func CheckNewTask(c NewTaskChoices) error {
	long := func(s string) bool { return len(s) > maxNewTaskText }
	if long(c.RunsOn) {
		return errors.New("new task: runs_on is too long")
	}
	if len(c.Hosts) > maxNewTaskHosts {
		return fmt.Errorf("new task: at most %d hosts", maxNewTaskHosts)
	}
	seen := map[string]bool{}
	for _, h := range c.Hosts {
		if seen[h.Host] {
			return fmt.Errorf("new task: host %q is given twice", h.Host)
		}
		seen[h.Host] = true
		if long(h.Host) || long(h.Model) || long(h.Effort) || h.Judge != nil && long(*h.Judge) {
			return errors.New("new task: a name is too long")
		}
		if h.Effort != "" && h.Model == "" {
			return errors.New("new task: an effort needs its model")
		}
		if len(h.Helpers) > maxNewTaskHelpers {
			return fmt.Errorf("new task: at most %d helpers", maxNewTaskHelpers)
		}
		for _, name := range h.Helpers {
			if name == "" || long(name) {
				return errors.New("new task: a helper needs a name")
			}
		}
		if len(h.Worktree) > maxNewTaskWorktrees {
			return fmt.Errorf("new task: at most %d worktree choices", maxNewTaskWorktrees)
		}
		for _, w := range h.Worktree {
			if !filepath.IsAbs(w.Folder) || long(w.Folder) {
				return errors.New("new task: a worktree choice's folder must be an absolute path")
			}
		}
	}
	return nil
}

// SaveNewTask replaces the choices for the next task.
func SaveNewTask(path string, c NewTaskChoices) error {
	if err := CheckNewTask(c); err != nil {
		return err
	}
	var b bytes.Buffer
	b.WriteString("# What the console chose last for a new task, which the next one starts\n")
	b.WriteString("# with. The daemon writes it; editing it by hand is fine.\n")
	if err := toml.NewEncoder(&b).Encode(c); err != nil {
		return fmt.Errorf("new task: %w", err)
	}
	return WriteFile(path, b.Bytes())
}
