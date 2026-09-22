package loop

import (
	"bytes"
	"encoding/json"

	"github.com/fclaude/nibb/internal/llm"
)

// turnProgress belongs to one turn. It observes completed calls, never grants
// authority, and retains no state across the user's next message (ADR 18).
type turnProgress struct {
	last     callKey
	repeated int
	errors   int
	denials  map[callKey]int
}

type callKey struct{ name, args string }
type toolOutcome struct{ failed, denied bool }

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

// Ignore JSON object ordering and whitespace, but preserve array order and
// exact numeric spellings; float conversion would merge large integer values.
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
