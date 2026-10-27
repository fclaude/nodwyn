package loop

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

// readOnlyTools are the model's tools that change nothing, on the machine or
// in the session, and are safe to run beside one another: reading files,
// listing and searching them, the outline of a file, and what git says of
// the work tree. A batch made only of these runs concurrently (ADR 57).
//
// The set is by name, since the primitive's declaration (scheme.Meta) has no
// such flag, and short on purpose. Left out, each for a reason:
//   - shell-exec, shell-start, shell-send, shell-stop, shell-read, the
//     term-* tools and the writing tools: they act, or read what a process
//     is doing, and the next call may depend on the last;
//   - index-search and index-update, which write the index they search;
//   - MCP tools, whose read-only hint is a server's claim, and the tools a
//     project defines in Scheme, which may do anything;
//   - scheme, which runs calls of its own.
var readOnlyTools = map[string]bool{
	"fs-read": true, "fs-list": true, "fs-find": true, "index-grep": true, "outline": true,
	"git-status": true, "git-diff": true, "git-head": true,
}

// maxConcurrentTools is how many calls of a batch run at once. A model sends
// a handful; the limit keeps a long batch from opening that many files and
// git processes together.
const maxConcurrentTools = 8

// concurrentBatch reports whether calls run concurrently: two or more, every
// one a read-only tool, and no two the same call. A call repeated is what
// the progress guard counts, and a repeat that ends the turn must find the
// calls after it unrun, as they are when the calls run in order.
func concurrentBatch(calls []llm.ToolCall) bool {
	if len(calls) < 2 {
		return false
	}
	seen := make(map[callKey]bool, len(calls))
	for _, tc := range calls {
		k := keyOf(tc)
		if !readOnlyTools[tc.Name] || seen[k] {
			return false
		}
		seen[k] = true
	}
	return true
}

// ranTool is what one call of a concurrent batch left behind.
type ranTool struct {
	started  bool // false: the turn was cancelled before the call began
	took     time.Duration
	produced scheme.Value
	err      error
}

// runConcurrently is runTools for a batch concurrentBatch accepted. Budget
// is reserved for the calls in order before any starts, so a limit that falls
// mid-batch leaves the same calls run as it does in order; the calls then run
// together, and are settled in order: the progress guard sees them, and their
// results are recorded and returned, as they would have been one by one.
// Those results do not depend on which call finished first.
//
// A call that ran has its real result recorded whatever happens to the calls
// beside it: when the progress guard stops the turn at one call, the ones
// after it have already run, and answering them as not executed would be
// untrue. A failed call stops nothing: they are independent.
func (l *Loop) runConcurrently(ctx context.Context, t *turnState, calls []llm.ToolCall) (scheme.Value, error) {
	if t.budget != nil {
		// A stop at the reservation of one call must not cancel the calls
		// before it, which fit the limit, before they begin.
		defer t.budget.Hold()()
	}
	ran, reserveErr := calls, error(nil)
	for i := range calls {
		if reserveErr = l.reserveCall(ctx, t); reserveErr != nil {
			ran = calls[:i]
			break
		}
	}
	rest := calls[len(ran):]
	results, err := l.runReserved(ctx, t, ran)
	if err != nil {
		if skipErr := l.skipTools(rest, err.Error()); skipErr != nil {
			return scheme.Void, skipErr
		}
		return scheme.Void, err
	}
	if reserveErr != nil {
		return scheme.Void, l.stop(reserveErr, rest)
	}
	return scheme.List(results...), nil
}

// runReserved records, runs and settles the calls, whose budget is reserved.
func (l *Loop) runReserved(ctx context.Context, t *turnState, calls []llm.ToolCall) ([]scheme.Value, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(calls))
	for _, tc := range calls {
		if err := l.recordToolCall(tc); err != nil {
			return nil, err
		}
		if len(names) == 0 || names[len(names)-1] != tc.Name {
			names = append(names, tc.Name)
		}
	}
	l.emitBusy(strings.Join(names, ", "))

	ran := make([]ranTool, len(calls))
	slots := make(chan struct{}, maxConcurrentTools)
	var wg sync.WaitGroup
	for i, tc := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return
			}
			if ctx.Err() != nil {
				return
			}
			start := time.Now()
			produced, err := l.apply(ctx, tc)
			ran[i] = ranTool{started: true, took: time.Since(start), produced: produced, err: err}
		}()
	}
	wg.Wait()

	results := make([]scheme.Value, 0, len(calls))
	for i, tc := range calls {
		r := ran[i]
		if !r.started {
			return nil, l.abandon(ctx, calls[i:], ran[i:], context.Cause(ctx))
		}
		res, value, outcome, err := l.finishTool(ctx, tc, r.took, r.produced, r.err)
		reason := ""
		if err == nil {
			// In the order of the calls, as the guard counts them, and before
			// the result is recorded, so that a warning goes with it.
			reason = t.progress.observe(tc, outcome)
			err = l.recordResult(res, t.progress.warned)
		}
		if err != nil {
			return nil, l.abandon(ctx, calls[i+1:], ran[i+1:], err)
		}
		results = append(results, resultValue(res, value, outcome))
		if reason != "" {
			stuck := &ProgressStop{Reason: reason}
			if err := l.record(log.KindNote, log.Note{Text: stuck.Error()}); err != nil {
				return nil, l.abandon(ctx, calls[i+1:], ran[i+1:], err)
			}
			return nil, l.abandon(ctx, calls[i+1:], ran[i+1:], halt(stuck))
		}
	}
	return results, nil
}

// abandon answers the calls left of a batch whose turn is ending with err:
// each that ran has its result recorded as it was, or as an interruption
// when the turn was cancelled under it, and each that never began is not
// executed. It returns err, or the failure to record.
func (l *Loop) abandon(ctx context.Context, calls []llm.ToolCall, ran []ranTool, err error) error {
	for i, tc := range calls {
		if !ran[i].started {
			if skipErr := l.skipTools(calls[i:i+1], err.Error()); skipErr != nil {
				return skipErr
			}
			continue
		}
		res, _, _, finishErr := l.finishTool(ctx, tc, ran[i].took, ran[i].produced, ran[i].err)
		if finishErr != nil {
			// Cancelled: finishTool has recorded the interruption.
			continue
		}
		if recordErr := l.recordResult(res, 0); recordErr != nil {
			return recordErr
		}
	}
	return err
}
