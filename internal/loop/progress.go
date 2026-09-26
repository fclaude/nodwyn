package loop

import (
	"bytes"
	"encoding/json"

	"github.com/fclaude/nibb/internal/llm"
)

// turnProgress notices a turn going in circles (ADR 18): the same call four
// times in a row, a denied call retried three times, ten failures in a row.
// It belongs to one turn, observes completed calls and never grants
// authority; the user's next message starts a fresh one.
type turnProgress struct {
	last     callKey         // the previous call
	repeated int             // how many times in a row last has run
	errors   int             // consecutive failed calls
	denials  map[callKey]int // denials per call, until it succeeds
}

// callKey identifies a call by what it does, not by its id.
type callKey struct{ name, args string }

// toolOutcome classifies a result: failed covers any error, denied only a
// guard refusal.
type toolOutcome struct{ failed, denied bool }

// observe records one completed call and returns why the turn should stop,
// or "" to go on.
func (p *turnProgress) observe(call llm.ToolCall, outcome toolOutcome) string {
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
	if p.repeated >= 4 {
		return "the same tool and arguments ran four times in a row"
	}
	if p.errors >= 10 {
		return "ten consecutive tool calls failed"
	}
	return ""
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
