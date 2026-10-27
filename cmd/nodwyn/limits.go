package main

import (
	"errors"
	"flag"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
)

// limitFlags are a task's own limits on the command line (ADR 49, amended
// 2026-10-27): each is none unless given, and 0 is none too, so a resumed
// task's limit is removed by giving it as 0. wall is the name of the
// per-turn time flag: line mode's is --budget-wall, as it always was, and a
// run's is --turn-wall, since its --budget-wall is the whole run's deadline.
type limitFlags struct {
	fs                         *flag.FlagSet
	wall                       string
	toolCalls, tokens, actions int64
	turnWall                   string
	cost                       float64
}

func addLimitFlags(fs *flag.FlagSet, wall string) *limitFlags {
	l := &limitFlags{fs: fs, wall: wall}
	fs.Int64Var(&l.toolCalls, "max-tool-calls", 0, "stop each turn of the task at this many tool calls (none unless given)")
	fs.Int64Var(&l.tokens, "budget-tokens", 0, "stop each turn of the task at this many model tokens (none unless given)")
	fs.Int64Var(&l.actions, "budget-actions", 0, "stop each turn of the task at this many writes and commands (none unless given)")
	fs.StringVar(&l.turnWall, wall, "", "stop each turn of the task after this long, e.g. 30m (none unless given)")
	fs.Float64Var(&l.cost, "budget-cost", 0, "stop the task once its model requests cost this many US dollars, as far as costs are known (none unless given)")
	return l
}

// given reports which of the limits the command line names, by flag.
func (l *limitFlags) given() map[string]bool {
	set := map[string]bool{}
	l.fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "max-tool-calls", "budget-tokens", "budget-actions", "budget-cost", l.wall:
			set[f.Name] = true
		}
	})
	return set
}

// over is was with each limit the command line names in its place: what a
// new task opens with (was none), or a resumed one goes on with.
func (l *limitFlags) over(was budget.Task) (budget.Task, error) {
	set := l.given()
	t := was
	if set["max-tool-calls"] {
		t.ToolCalls = l.toolCalls
	}
	if set["budget-tokens"] {
		t.Tokens = l.tokens
	}
	if set["budget-actions"] {
		t.Actions = l.actions
	}
	if set["budget-cost"] {
		t.Cost = l.cost
	}
	if set[l.wall] {
		t.Wall = 0
		if l.turnWall != "0" {
			d, err := time.ParseDuration(l.turnWall)
			if err != nil || d < 0 {
				return budget.Task{}, errors.New("--" + l.wall + " is a duration, such as 30m, or 0 for none")
			}
			t.Wall = int64(d)
		}
	}
	if err := t.Validate(); err != nil {
		return budget.Task{}, errors.New("limits: " + err.Error())
	}
	return t, nil
}
