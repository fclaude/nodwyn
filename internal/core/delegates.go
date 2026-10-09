package core

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/loop"
)

// DelegateChoice names the configured models a task's model may hand work to
// (ADR 27, amended 2026-10-09), and who chose them, for the log.
type DelegateChoice struct {
	Models []string
	Actor  string
}

// checkDelegates says why names cannot be a task's delegates, or returns them
// sorted without repeats. The model the task talks to needs no listing: it is
// the one delegating, and "control" is that model.
func checkDelegates(c config.Config, names []string) ([]string, error) {
	var out []string
	for _, n := range names {
		if _, ok := c.Models[n]; !ok || n == loop.ControlModel {
			return nil, fmt.Errorf("delegates: no model %q is configured; configured models: %s", n, strings.Join(slices.Sorted(maps.Keys(c.Models)), ", "))
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// initialDelegates are the models a session opened with: those it was given,
// else none.
func initialDelegates(o Options) []string {
	if o.Delegates == nil {
		return nil
	}
	return o.Delegates.Models
}

// keepConfigured is names without those config.toml no longer has.
func keepConfigured(c config.Config, names []string) []string {
	var out []string
	for _, n := range names {
		if _, ok := c.Models[n]; ok && n != loop.ControlModel {
			out = append(out, n)
		}
	}
	return out
}

// Delegates are the models the task may hand work to, sorted.
func (s *Session) Delegates() []string { return slices.Clone(s.delegates) }

// lastDelegates is the delegates the log chose for this task, if it chose
// any: the choice outlives the process, as the task does.
func lastDelegates(events []log.Event) (rec log.SessionDelegates, found bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != log.KindSessionDelegates {
			continue
		}
		if err := events[i].Decode(&rec); err != nil {
			return log.SessionDelegates{}, false
		}
		return rec, true
	}
	return log.SessionDelegates{}, false
}

// SetLabels gives the configured models the labels c holds now, so a label
// edited in Settings reaches a task that is already open. Only labels move:
// where a model's requests go, and the tools offered, are fixed for the
// session.
func (s *Session) SetLabels(c config.Config) {
	labels := make(map[string][]string, len(c.Models))
	for name, m := range c.Models {
		labels[name] = c.LabelsOf(m)
	}
	s.loop.SetLabels(labels)
}
