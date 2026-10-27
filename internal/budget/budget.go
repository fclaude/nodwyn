// Package budget bounds one supervised turn (ADR 18). It owns accounting and
// checkpoint choices; the session supplies the prompt and durable-recording
// callback.
//
// A turn is measured in four dimensions: tool calls, model tokens,
// irreversible actions and wall time. Each has a soft limit, which raises a
// checkpoint asking whether to continue, and a hard limit, which stops the
// turn and cancels its context. A fifth, spend, counts the known cost of the
// whole task, not one turn. A dimension whose hard limit is 0 has no limit.
// A task's own limits (Task) are none unless a person set them, and set
// each soft limit at its hard one, so they stop without asking (ADR 49,
// amended 2026-10-27).
package budget

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Limit is one dimension's ceilings. Wall limits are nanoseconds; cost
// limits are millionths of a US dollar (Micro).
type Limit struct {
	Soft int64 `json:"soft"` // reaching it raises a checkpoint
	Hard int64 `json:"hard"` // reaching it stops the turn
}

// Limits are a turn's ceilings in every dimension. A dimension is off when
// its Hard is 0.
type Limits struct{ ToolCalls, Tokens, Actions, Wall, Cost Limit }

// Micro is one US dollar in a cost limit's unit.
const Micro = 1_000_000

// Micros converts US dollars to a cost limit's unit.
func Micros(usd float64) int64 { return int64(math.Round(usd * Micro)) }

// Amount writes a dimension's value as a person reads it: a duration for
// wall time, dollars for cost, a count otherwise.
func Amount(dimension string, n int64) string {
	switch dimension {
	case "wall":
		return time.Duration(n).String()
	case "cost":
		usd := float64(n) / Micro
		if usd != 0 && usd < 0.01 {
			return fmt.Sprintf("$%.4f", usd)
		}
		return fmt.Sprintf("$%.2f", usd)
	}
	return strconv.FormatInt(n, 10)
}

// State is what a turn has used so far, shown at a checkpoint. Files and
// Commands are the distinct targets of its irreversible actions.
type State struct {
	ToolCalls int64         `json:"tool_calls"`
	Tokens    int64         `json:"tokens"`
	Actions   int64         `json:"actions"`
	Elapsed   time.Duration `json:"elapsed_ns"`
	Files     []string      `json:"files"`
	Commands  []string      `json:"commands"`
	// What the turn's model requests cost, as far as known: Cost in USD
	// over the Priced requests, Unpriced the requests whose cost is not
	// known, CachedTokens the prompt tokens providers said came from their
	// cache. A spend limit, when set, counts Cost (with EarlierCost); Tokens
	// still counts cached tokens in full.
	Cost         float64 `json:"cost,omitempty"`
	Priced       int64   `json:"priced,omitempty"`
	Unpriced     int64   `json:"unpriced,omitempty"`
	CachedTokens int64   `json:"cached_tokens,omitempty"`
	// EarlierCost is what the task's earlier turns cost, as far as known: a
	// spend limit counts it with Cost.
	EarlierCost float64 `json:"earlier_cost,omitempty"`
	// The judge's requests this turn: how many answered, their tokens, and
	// their cost as far as priced ([judge] price_*, else its provider's), JudgeUnpriced the ones
	// not priced. Judge tokens are not model tokens, so the token budget
	// leaves them out; a spend limit counts JudgeCost.
	JudgeCalls    int64   `json:"judge_calls,omitempty"`
	JudgeTokens   int64   `json:"judge_tokens,omitempty"`
	JudgeCost     float64 `json:"judge_cost,omitempty"`
	JudgeUnpriced int64   `json:"judge_unpriced,omitempty"`
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
// limit. Wall limits use durations, cost limits US dollars ("2.50" or
// "$2.50"); other dimensions use positive integers.
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
	switch p.Dimension {
	case "wall":
		var d time.Duration
		d, err = time.ParseDuration(reply)
		next = int64(d)
	case "cost":
		var usd float64
		usd, err = strconv.ParseFloat(strings.TrimPrefix(reply, "$"), 64)
		if err == nil && (math.IsNaN(usd) || math.IsInf(usd, 0)) {
			err = errors.New("not a number")
		}
		next = Micros(usd)
	default:
		next, err = strconv.ParseInt(reply, 10, 64)
	}
	if err != nil || next <= p.Used || next <= p.Limit.Soft || next > p.Limit.Hard {
		return Choice{}, errors.New("choose y, n, or a soft limit above current usage and the previous soft limit, at most the hard limit")
	}
	return Choice{true, next}, nil
}

// Stop distinguishes a hard turn ceiling from a declined checkpoint. Drivers
// may start a new bounded turn after the former, never after the latter.
// Spend marks the hard spend limit, which is the task's: no later turn may
// start after it either, since it would start past the limit.
type Stop struct {
	Hard   bool
	Spend  bool
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
// blocked model calls, tools and confirmations; without one, a turn may wait
// for a person as long as it takes. Limits must be validated by
// configuration. Call the returned cleanup function on every exit.
func Start(parent context.Context, limits Limits, confirm Confirm) (context.Context, *Turn, context.CancelFunc) {
	if confirm == nil {
		confirm = func(context.Context, Checkpoint) (Choice, error) { return Choice{}, nil }
	}
	deadline, stopTimer := parent, context.CancelFunc(func() {})
	if limits.Wall.Hard > 0 {
		deadline, stopTimer = context.WithTimeoutCause(parent, time.Duration(limits.Wall.Hard), &Stop{Hard: true, Reason: "hard stop: wall budget reached"})
	}
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

// hard stops the turn if used has reached the hard limit, when there is one.
func (t *Turn) hard(ctx context.Context, dimension string, used int64, limit Limit) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if limit.Hard > 0 && used >= limit.Hard {
		if dimension == "cost" {
			err := &Stop{Hard: true, Spend: true, Reason: "hard stop: spend limit reached (" + Amount("cost", used) + " of " + Amount("cost", limit.Hard) + " known)"}
			t.cancel(err)
			return err
		}
		return t.stop("hard stop: "+dimension+" budget reached", true)
	}
	return nil
}

// spent is the task's known cost so far, the judge's included, in the cost
// limit's unit.
func (t *Turn) spent() int64 {
	return Micros(t.state.EarlierCost + t.state.Cost + t.state.JudgeCost)
}

// AddJudge records one judge request: its tokens, and its cost (nil: not
// priced). Like AddSpend it stops nothing itself.
func (t *Turn) AddJudge(tokens int, cost *float64) {
	t.state.JudgeCalls++
	if tokens > 0 {
		t.state.JudgeTokens += int64(tokens)
	}
	if cost != nil && *cost >= 0 {
		t.state.JudgeCost += *cost
	} else {
		t.state.JudgeUnpriced++
	}
}

// Snapshot is what the turn has used so far.
func (t *Turn) Snapshot() State { return t.snapshot() }

// SetEarlierCost tells the turn what the task's earlier turns cost, for the
// spend limit. Call it before the turn's work.
func (t *Turn) SetEarlierCost(usd float64) { t.state.EarlierCost = usd }

// Spend is what the turn's model requests cost, as far as known, how many
// were unpriced, and the spend limit as the turn leaves it (a checkpoint may
// have raised its soft limit). Call it once the turn's work is over.
func (t *Turn) Spend() (cost float64, unpriced int64, limit Limit) {
	return t.state.Cost, t.state.Unpriced, t.limits.Cost
}

// check applies both limits: past the hard one the turn stops; past the
// soft one the user is asked, and may raise it towards the hard one.
func (t *Turn) check(ctx context.Context, dimension string, used int64, limit *Limit) error {
	if err := t.hard(ctx, dimension, used, *limit); err != nil {
		return err
	}
	// Counts permit exactly Soft operations; elapsed time/tokens are measured
	// after work, so reaching their soft boundary is enough for a checkpoint.
	// A dimension with no limit asks nothing.
	crossed := used >= limit.Soft
	if limit.Hard <= 0 || !crossed || limit.Soft >= limit.Hard {
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
	if err := t.check(ctx, "tokens", t.state.Tokens, &t.limits.Tokens); err != nil {
		return err
	}
	if t.limits.Cost.Hard > 0 {
		return t.check(ctx, "cost", t.spent(), &t.limits.Cost)
	}
	return nil
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

// cachedTokenShare is how many cached prompt tokens the token budget counts
// as one: a provider reads them from its prompt cache instead of processing
// them again, and prices them at about a tenth. Counted in full, a long
// conversation on a model with a large context reached the default soft
// limit in minutes while nine tenths of what it counted came from cache.
const cachedTokenShare = 10

// CountedPrompt is how many of a request's prompt tokens the token budget
// counts, cached of them read from the provider's prompt cache (nil: not
// reported, and all of them count).
func CountedPrompt(prompt int, cached *int) int {
	if cached == nil || *cached <= 0 || prompt <= 0 {
		return prompt
	}
	c := min(*cached, prompt)
	return prompt - c + c/cachedTokenShare
}

// AddTokens accounts for observed usage, prompt as CountedPrompt counts it.
// Check before further work; Finish checks hard ceilings without asking to
// continue an already completed answer.
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

// AddSpend records one model request's cost (nil: not known) and the prompt
// tokens its provider read from cache (nil: not reported). It stops nothing
// itself: the next Check or Finish applies the spend limit. A cost not known
// is not counted, so a spend limit sees only priced requests.
func (t *Turn) AddSpend(cost *float64, cached *int) {
	if cost != nil && *cost >= 0 {
		t.state.Cost += *cost
		t.state.Priced++
	} else {
		t.state.Unpriced++
	}
	if cached != nil && *cached > 0 {
		t.state.CachedTokens += int64(*cached)
	}
}

// Finish enforces observed hard ceilings even when the model has finished, but
// does not request additional soft allowance when there is no remaining work.
func (t *Turn) Finish(ctx context.Context) error {
	if err := t.hard(ctx, "wall", int64(time.Since(t.start)), t.limits.Wall); err != nil {
		return err
	}
	if err := t.hard(ctx, "tokens", t.state.Tokens, t.limits.Tokens); err != nil {
		return err
	}
	if t.limits.Cost.Hard > 0 {
		return t.hard(ctx, "cost", t.spent(), t.limits.Cost)
	}
	return nil
}
