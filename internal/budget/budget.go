// Package budget bounds one supervised turn (ADR 18). It owns accounting and
// checkpoint choices; the session supplies the prompt and durable-recording
// callback.
//
// A turn is measured in four dimensions: tool calls, model tokens,
// irreversible actions and wall time. Each has a soft limit, which raises a
// checkpoint asking whether to continue, and a hard limit, which stops the
// turn and cancels its context.
package budget

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

// Limit is one dimension's ceilings. Wall limits are nanoseconds.
type Limit struct {
	Soft int64 `json:"soft"` // reaching it raises a checkpoint
	Hard int64 `json:"hard"` // reaching it stops the turn
}

// Limits are a turn's ceilings in every dimension.
type Limits struct{ ToolCalls, Tokens, Actions, Wall Limit }

// State is what a turn has used so far, shown at a checkpoint. Files and
// Commands are the distinct targets of its irreversible actions.
type State struct {
	ToolCalls int64         `json:"tool_calls"`
	Tokens    int64         `json:"tokens"`
	Actions   int64         `json:"actions"`
	Elapsed   time.Duration `json:"elapsed_ns"`
	Files     []string      `json:"files"`
	Commands  []string      `json:"commands"`
}

// Checkpoint is the question put to the user when a soft limit is reached:
// this dimension has used this much of this limit; continue?
type Checkpoint struct {
	Dimension string `json:"dimension"`
	Used      int64  `json:"used"`
	Limit     Limit  `json:"limit"`
	State     State  `json:"state"`
}

// Choice answers a Checkpoint: stop, or continue up to a new soft limit.
type Choice struct {
	Continue bool  `json:"continue"`
	Soft     int64 `json:"soft"`
}

// ParseChoice accepts yes (double the soft limit), no, or a new absolute soft
// limit. Wall limits use durations; other dimensions use positive integers.
func ParseChoice(reply string, p Checkpoint) (Choice, error) {
	reply = strings.ToLower(strings.TrimSpace(reply))
	switch reply {
	case "", "n", "no", "stop":
		return Choice{}, nil
	case "y", "yes", "continue":
		next := p.Limit.Hard
		if p.Limit.Soft <= p.Limit.Hard-p.Limit.Soft {
			next = p.Limit.Soft * 2
		}
		if next <= p.Used && p.Used < p.Limit.Hard {
			next = p.Used + 1
		}
		return Choice{true, next}, nil
	}
	var next int64
	var err error
	if p.Dimension == "wall" {
		var d time.Duration
		d, err = time.ParseDuration(reply)
		next = int64(d)
	} else {
		next, err = strconv.ParseInt(reply, 10, 64)
	}
	if err != nil || next <= p.Used || next <= p.Limit.Soft || next > p.Limit.Hard {
		return Choice{}, errors.New("choose y, n, or a soft limit above current usage and the previous soft limit, at most the hard limit")
	}
	return Choice{true, next}, nil
}

// Stop distinguishes a hard turn ceiling from a declined checkpoint. Drivers
// may start a new bounded turn after the former, never after the latter.
type Stop struct {
	Hard   bool
	Reason string
}

func (s *Stop) Error() string { return s.Reason }

// Confirm asks the user about a checkpoint. The session implements it with
// the same prompt channel the guard uses, so any front-end can answer.
type Confirm func(context.Context, Checkpoint) (Choice, error)

// Turn accounts for one turn. It travels in the turn's context (From), so
// the loop and the guard's gate charge the same budget.
type Turn struct {
	limits  Limits
	state   State
	start   time.Time
	confirm Confirm
	cancel  context.CancelCauseFunc
}
type contextKey struct{}

// Start binds the budget to the turn context. A hard wall deadline also cancels
// blocked model calls, tools and confirmations. Limits must be validated by
// configuration. Call the returned cleanup function on every exit.
func Start(parent context.Context, limits Limits, confirm Confirm) (context.Context, *Turn, context.CancelFunc) {
	if confirm == nil {
		confirm = func(context.Context, Checkpoint) (Choice, error) { return Choice{}, nil }
	}
	deadline, stopTimer := context.WithTimeoutCause(parent, time.Duration(limits.Wall.Hard), &Stop{Hard: true, Reason: "hard stop: wall budget reached"})
	ctx, cancel := context.WithCancelCause(deadline)
	t := &Turn{limits: limits, start: time.Now(), confirm: confirm, cancel: cancel}
	return context.WithValue(ctx, contextKey{}, t), t, func() {
		cancel(nil)
		stopTimer()
	}
}

// From returns the budget Start bound to ctx, or nil if there is none.
func From(ctx context.Context) *Turn {
	t, _ := ctx.Value(contextKey{}).(*Turn)
	return t
}

func (t *Turn) snapshot() State {
	s := t.state
	s.Elapsed = time.Since(t.start)
	s.Files = append([]string(nil), s.Files...)
	s.Commands = append([]string(nil), s.Commands...)
	return s
}

// stop ends the turn: it cancels the turn's context with the Stop as cause.
func (t *Turn) stop(reason string, hard bool) error {
	err := &Stop{Hard: hard, Reason: reason}
	t.cancel(err)
	return err
}

// hard stops the turn if used has reached the hard limit.
func (t *Turn) hard(ctx context.Context, dimension string, used int64, limit Limit) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if used >= limit.Hard {
		return t.stop("hard stop: "+dimension+" budget reached", true)
	}
	return nil
}

// check applies both limits: past the hard one the turn stops; past the
// soft one the user is asked, and may raise it towards the hard one.
func (t *Turn) check(ctx context.Context, dimension string, used int64, limit *Limit) error {
	if err := t.hard(ctx, dimension, used, *limit); err != nil {
		return err
	}
	// Counts permit exactly Soft operations; elapsed time/tokens are measured
	// after work, so reaching their soft boundary is enough for a checkpoint.
	crossed := used >= limit.Soft
	if !crossed || limit.Soft >= limit.Hard {
		return nil
	}
	p := Checkpoint{Dimension: dimension, Used: used, Limit: *limit, State: t.snapshot()}
	choice, err := t.confirm(ctx, p)
	if err != nil {
		t.cancel(err)
		return err
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if !choice.Continue {
		return t.stop("checkpoint stopped: "+dimension+" budget", false)
	}
	if choice.Soft <= used || choice.Soft <= limit.Soft || choice.Soft > limit.Hard {
		return t.stop("checkpoint stopped: invalid budget choice", false)
	}
	limit.Soft = choice.Soft
	return nil
}

// Check runs at operation boundaries; the hard wall deadline runs continuously.
func (t *Turn) Check(ctx context.Context) error {
	if err := t.Finish(ctx); err != nil {
		return err
	}
	if err := t.check(ctx, "wall", int64(time.Since(t.start)), &t.limits.Wall); err != nil {
		return err
	}
	return t.check(ctx, "tokens", t.state.Tokens, &t.limits.Tokens)
}

// ToolCall charges one tool call, checking every limit first.
func (t *Turn) ToolCall(ctx context.Context) error {
	if err := t.hard(ctx, "tool_calls", t.state.ToolCalls, t.limits.ToolCalls); err != nil {
		return err
	}
	if err := t.Check(ctx); err != nil {
		return err
	}
	if err := t.check(ctx, "tool_calls", t.state.ToolCalls, &t.limits.ToolCalls); err != nil {
		return err
	}
	t.state.ToolCalls++
	return nil
}

// Action charges one irreversible action — a write to path or a run of
// command — checking every limit first.
func (t *Turn) Action(ctx context.Context, path, command string) error {
	if err := t.hard(ctx, "actions", t.state.Actions, t.limits.Actions); err != nil {
		return err
	}
	if err := t.Check(ctx); err != nil {
		return err
	}
	if err := t.check(ctx, "actions", t.state.Actions, &t.limits.Actions); err != nil {
		return err
	}
	t.state.Actions++
	if path != "" {
		t.state.Files = uniqueAppend(t.state.Files, path)
	}
	if command != "" {
		t.state.Commands = uniqueAppend(t.state.Commands, command)
	}
	return nil
}

func uniqueAppend(items []string, item string) []string {
	for _, old := range items {
		if old == item {
			return items
		}
	}
	return append(items, item)
}

// AddTokens accounts for observed usage. Check before further work; Finish
// checks hard ceilings without asking to continue an already completed answer.
func (t *Turn) AddTokens(prompt, output int) {
	for _, n := range []int{prompt, output} {
		if n > 0 {
			if int64(n) > math.MaxInt64-t.state.Tokens {
				t.state.Tokens = math.MaxInt64
			} else {
				t.state.Tokens += int64(n)
			}
		}
	}
}

// Finish enforces observed hard ceilings even when the model has finished, but
// does not request additional soft allowance when there is no remaining work.
func (t *Turn) Finish(ctx context.Context) error {
	if err := t.hard(ctx, "wall", int64(time.Since(t.start)), t.limits.Wall); err != nil {
		return err
	}
	return t.hard(ctx, "tokens", t.state.Tokens, t.limits.Tokens)
}
