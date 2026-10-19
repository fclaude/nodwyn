package loop

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/fclaude/nodwyn/internal/llm"
)

// ProgressStop is the error a turn ends with when the progress guard stops
// it. It is a type so the session can tell it from other errors: like a hard
// limit, it ends a turn that has stopped getting anywhere (ADR 18, amended
// 2026-10-06).
type ProgressStop struct{ Reason string }

func (e *ProgressStop) Error() string {
	return "progress stop: " + e.Reason + "; inspect the results before continuing"
}

// turnProgress notices a turn going in circles (ADR 18): the same call four
// times in a row, a denied call retried three times, ten failures in a row,
// the same tool failing on the same file in six of the last sixteen model
// responses, a command that is still running polled thirty times in a row.
// It belongs to one turn, observes completed calls and never grants
// authority; the user's next message starts a fresh one.
type turnProgress struct {
	last     callKey         // the previous call
	repeated int             // how many times in a row last has run
	errors   int             // consecutive failed calls
	denials  map[callKey]int // denials per call, until it succeeds
	round    int             // model responses so far this turn
	failures []failure       // failed calls within the last stallWindow responses
	polls    int             // consecutive reads of a command that was still running
	screens  int             // consecutive calls that drove a terminal
	// warnRepeats makes the same call run repeats times in a row a warning
	// to the model rather than a stop (sandbox mode, ADR 55).
	warnRepeats bool
	// warned is, under warnRepeats, how many times in a row the call observed
	// last has run when that is a multiple of repeats, else 0: its result
	// then carries repeatNote(warned).
	warned int
}

// repeats is how many runs in a row of the same call end the turn. In
// sandbox mode (ADR 55, amended 2026-10-19) they do not: the result of every
// repeats-th run says so to the model and the turn goes on. A run there is
// bounded by its wall-clock and tool-call budgets, and waiting on a long job
// with one command is legitimate: a task that trained a model in the
// background for about fifty minutes polled its log with the same command
// line (sleep, tail, date), and the stop ended the whole run at minute 52 of
// 57, before the training had finished.
const repeats = 4

// repeatNote is what, in sandbox mode, the result of a call that has run n
// times in a row says to the model after the call's own output.
func repeatNote(n int) string {
	return fmt.Sprintf("[this exact call has now run %d times in a row; if you are waiting on something, that is fine; otherwise look at what the results show before running it again]", n)
}

// pollLimit is how many reads of a command that is still running, with
// nothing else between them, end the turn. Waiting for one is not going in
// circles: a poll that waits up to a minute is the model letting the
// command work, and the same poll four times in a row ended two turns
// that had done nothing wrong (20261003T002506Z-f564a28c seq 5889 to
// 5907, a promote that had succeeded; 20261003T143916Z-48f31857 seq 7323
// to 7341, a browser check that was still running). Thirty polls of up to
// a minute is half an hour: a command that hangs still ends the turn.
const pollLimit = 30

// screenLimit is how many calls that drive a terminal, with nothing else
// between them, end the turn. A program driven through a terminal takes many
// small calls (a key, a look at the screen, another key), and the same key
// pressed four times in a row is how one moves down a file, so these calls
// are not counted as repeats (ADR 48). The number is a guess, as the terminal
// is experimental: no session log has driven one yet.
const screenLimit = 150

// A stall is the same tool failing on the same target in stallFailures of the
// last stallWindow model responses. Reads, successful calls and edits that
// differ in their text do not reset it: a model sure that a file holds what it
// does not alternates a failing edit with a read and a small change, and no
// streak of identical or consecutive calls shows it. A response counts once
// however many of its calls failed, since a batch repeats one mistake in
// parallel. The numbers come from the 73 session logs on one machine
// (2026-10-05): six of sixteen fires once, on the stall this is for, five
// also fires on a turn that recovered.
const (
	stallWindow   = 16
	stallFailures = 6
)

// failure is one failed call: the target it was made on and the model
// response that made it.
type failure struct {
	target string
	round  int
}

// callKey identifies a call by what it does, not by its id.
type callKey struct{ name, args string }

// toolOutcome classifies a result: failed covers any error, denied only a
// guard refusal, waiting a read of a background command that is still
// running, terminal a call that pressed a terminal's keys or read its screen.
type toolOutcome struct{ failed, denied, waiting, terminal bool }

// response notes that the model has answered again, which is the unit a
// stall is counted in.
func (p *turnProgress) response() { p.round++ }

// observe records one completed call and returns why the turn should stop,
// or "" to go on; under warnRepeats it sets warned for a call that goes on.
func (p *turnProgress) observe(call llm.ToolCall, outcome toolOutcome) string {
	p.warned = 0
	if outcome.waiting && !outcome.failed {
		// Waiting for a command is not repeating a call: the polls are
		// counted on their own, and the repeat count is left as it was.
		p.polls++
		if p.polls >= pollLimit {
			return fmt.Sprintf("a command that is still running was read %d times in a row", p.polls)
		}
		p.errors = 0
		return ""
	}
	if outcome.terminal && !outcome.failed {
		p.screens++
		if p.screens >= screenLimit {
			return fmt.Sprintf("a terminal was driven %d times in a row", p.screens)
		}
		p.errors = 0
		return ""
	}
	p.polls, p.screens = 0, 0
	key := keyOf(call)
	if key == p.last {
		p.repeated++
	} else {
		p.last, p.repeated = key, 1
	}
	if outcome.failed {
		p.errors++
	} else {
		p.errors = 0
	}
	if outcome.denied {
		if p.denials == nil {
			p.denials = make(map[callKey]int)
		}
		p.denials[key]++
		// The original denial plus three retries, even with other calls between.
		if p.denials[key] >= 4 {
			return "a denied call was retried three times"
		}
	} else if !outcome.failed {
		// A later successful approval means the old denial is no longer relevant.
		delete(p.denials, key)
	}
	if outcome.failed {
		if reason := p.stalled(call); reason != "" {
			return reason
		}
	}
	if p.repeated >= repeats && !p.warnRepeats {
		return "the same tool and arguments ran four times in a row"
	}
	if p.errors >= 10 {
		return "ten consecutive tool calls failed"
	}
	if p.warnRepeats && p.repeated%repeats == 0 {
		p.warned = p.repeated
	}
	return ""
}

// stalled records a failed call and says whether its target has now failed in
// stallFailures of the last stallWindow responses.
func (p *turnProgress) stalled(call llm.ToolCall) string {
	target := targetOf(call)
	kept := p.failures[:0]
	for _, f := range p.failures {
		if f.round > p.round-stallWindow {
			kept = append(kept, f)
		}
	}
	p.failures = append(kept, failure{target, p.round})
	rounds, last := 0, -1
	for _, f := range p.failures {
		if f.target == target && f.round != last {
			rounds++
			last = f.round
		}
	}
	if rounds < stallFailures {
		return ""
	}
	return fmt.Sprintf("%s failed in %d of the last %d model responses", target, rounds, stallWindow)
}

// targetOf is what a call acts on: the tool and its path argument, or, for a
// tool that takes none, the call itself.
func targetOf(call llm.ToolCall) string {
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(call.Args, &args) == nil && args.Path != "" {
		return call.Name + " on " + args.Path
	}
	key := keyOf(call)
	return "the call " + key.name + " " + key.args
}

// keyOf ignores JSON object ordering and whitespace, but preserves array
// order and exact numeric spellings; float conversion would merge large
// integer values.
func keyOf(call llm.ToolCall) callKey {
	args := call.Args
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.UseNumber()
	var value any
	if json.Valid(args) && decoder.Decode(&value) == nil {
		args, _ = json.Marshal(value)
	}
	return callKey{call.Name, string(args)}
}
