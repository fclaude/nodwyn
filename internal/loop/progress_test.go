package loop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/llm"
)

func call(name, args string) llm.ToolCall {
	return llm.ToolCall{Name: name, Args: json.RawMessage(args)}
}

func TestProgressRepeatedCalls(t *testing.T) {
	var p turnProgress
	for i, args := range []string{`{"a":1,"b":2}`, `{ "b": 2, "a": 1 }`, `{"a":1,"b":2}`, `{"b":2,"a":1}`} {
		reason := p.observe(call("read", args), toolOutcome{})
		if (reason != "") != (i == 3) {
			t.Fatalf("call %d: %q", i, reason)
		}
	}
	// Success with different arguments resets the repetition count.
	if reason := p.observe(call("read", `{"a":2}`), toolOutcome{}); reason != "" {
		t.Fatal(reason)
	}
}

func TestProgressDeniedRetriesAcrossOtherCalls(t *testing.T) {
	var p turnProgress
	for i := 0; i < 4; i++ {
		p.observe(call("read", `{}`), toolOutcome{})
		reason := p.observe(call("write", `{"path":"secret"}`), toolOutcome{failed: true, denied: true})
		if (reason != "") != (i == 3) {
			t.Fatalf("denial %d: %q", i, reason)
		}
		if i == 3 && !strings.Contains(reason, "denied") {
			t.Fatal(reason)
		}
	}
	// Successful execution clears historical denials for that exact call.
	p.observe(call("write", `{"path":"secret"}`), toolOutcome{})
	if reason := p.observe(call("write", `{"path":"secret"}`), toolOutcome{failed: true, denied: true}); reason != "" {
		t.Fatal(reason)
	}
}

func TestProgressConsecutiveErrors(t *testing.T) {
	var p turnProgress
	for i := 0; i < 9; i++ {
		p.observe(call("missing", string(rune('0'+i))), toolOutcome{failed: true})
	}
	// A successful call breaks the error streak.
	if reason := p.observe(call("ok", `{}`), toolOutcome{}); reason != "" {
		t.Fatal(reason)
	}
	for i := 0; i < 10; i++ {
		args, _ := json.Marshal(i)
		reason := p.observe(call("missing", string(args)), toolOutcome{failed: true})
		if (reason != "") != (i == 9) {
			t.Fatalf("failure %d: %q", i, reason)
		}
		if i == 9 && !strings.Contains(reason, "ten consecutive") {
			t.Fatal(reason)
		}
	}
}

func TestCallIdentity(t *testing.T) {
	for _, pair := range [][2]llm.ToolCall{
		{call("read", `[1,2]`), call("read", `[2,1]`)},
		{call("read", `9007199254740992`), call("read", `9007199254740993`)},
		{call("read", `{}`), call("write", `{}`)},
	} {
		if keyOf(pair[0]) == keyOf(pair[1]) {
			t.Fatal("different calls compare equal")
		}
	}
	if keyOf(call("read", `{"a":`)).args != `{"a":` {
		t.Fatal("malformed arguments changed")
	}
}
