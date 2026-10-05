package loop

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/fclaude/nodwyn/internal/llm"
)

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
// running.
type toolOutcome struct{ failed, denied, waiting bool }

// response notes that the model has answered again, which is the unit a
// stall is counted in.
func (p *turnProgress) response() { p.round++ }

// observe records one completed call and returns why the turn should stop,
// or "" to go on.
func (p *turnProgress) observe(call llm.ToolCall, outcome toolOutcome) string {
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
	p.polls = 0
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
	if p.repeated >= 4 {
		return "the same tool and arguments ran four times in a row"
	}
	if p.errors >= 10 {
		return "ten consecutive tool calls failed"
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
