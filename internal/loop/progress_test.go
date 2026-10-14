package loop

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/llm"
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

// observeRound is a model response whose calls are observed in order.
func observeRound(p *turnProgress, calls ...func() (llm.ToolCall, toolOutcome)) string {
	p.response()
	for _, c := range calls {
		if reason := p.observe(c()); reason != "" {
			return reason
		}
	}
	return ""
}

func failing(name, args string) func() (llm.ToolCall, toolOutcome) {
	return func() (llm.ToolCall, toolOutcome) { return call(name, args), toolOutcome{failed: true} }
}

func succeeding(name, args string) func() (llm.ToolCall, toolOutcome) {
	return func() (llm.ToolCall, toolOutcome) { return call(name, args), toolOutcome{} }
}

// A turn that keeps failing on one file stops, however much it reads and
// whatever the edits' text: none of that is a streak of identical or
// consecutive calls.
func TestProgressStallOnOneFile(t *testing.T) {
	var p turnProgress
	for i := 0; i < stallFailures; i++ {
		edit := failing("fs-edit", fmt.Sprintf(`{"path":"a.go","old":"v%d","new":"w"}`, i))
		reason := observeRound(&p, edit)
		if (reason != "") != (i == stallFailures-1) {
			t.Fatalf("failure %d: %q", i, reason)
		}
		if i == stallFailures-1 && !strings.Contains(reason, "fs-edit on a.go failed in 6 of the last 16 model responses") {
			t.Fatal(reason)
		}
		if reason == "" {
			if r := observeRound(&p, succeeding("fs-read", fmt.Sprintf(`{"path":"a.go","start":%d}`, i))); r != "" {
				t.Fatalf("a read stopped the turn: %q", r)
			}
		}
	}
}

// Failures that are not the same tool on the same file, are spread out, or
// come from one response are not a stall.
func TestProgressStallNeedsOneTargetInOneWindow(t *testing.T) {
	var p turnProgress
	for i := 0; i < 3*stallWindow; i++ {
		path := fmt.Sprintf(`{"path":"f%d.go"}`, i)
		if r := observeRound(&p, failing("fs-edit", path), succeeding("fs-list", `{"path":"."}`)); r != "" {
			t.Fatalf("failures on different files: %q", r)
		}
	}
	p = turnProgress{}
	for i := 0; i < stallFailures-1; i++ {
		observeRound(&p, failing("fs-edit", fmt.Sprintf(`{"path":"a.go","old":"%d"}`, i)))
	}
	// Another tool's failure on the same file, and a successful edit of it, add nothing.
	observeRound(&p, failing("fs-write", `{"path":"a.go"}`), succeeding("fs-edit", `{"path":"a.go"}`))
	// The failures fall out of the window before the sixth.
	for i := 0; i < stallWindow; i++ {
		if r := observeRound(&p, succeeding("fs-list", fmt.Sprintf(`{"path":"d%d"}`, i))); r != "" {
			t.Fatal(r)
		}
	}
	if r := observeRound(&p, failing("fs-edit", `{"path":"a.go","old":"late"}`)); r != "" {
		t.Fatalf("old failures were counted: %q", r)
	}
	// One response failing six times counts once: a batch repeats one mistake.
	p = turnProgress{}
	var batch []func() (llm.ToolCall, toolOutcome)
	for i := 0; i < stallFailures; i++ {
		batch = append(batch, failing("fs-edit", fmt.Sprintf(`{"path":"a.go","old":"%d"}`, i)))
	}
	if r := observeRound(&p, batch...); r != "" {
		t.Fatalf("a batch of failures in one response: %q", r)
	}
}

// A tool without a path is judged by the call itself.
func TestProgressStallWithoutPath(t *testing.T) {
	var p turnProgress
	for i := 0; i < stallFailures; i++ {
		reason := observeRound(&p, failing("mcp-call", `{"tool":"x"}`))
		if i < stallFailures-1 {
			if reason != "" {
				t.Fatalf("failure %d: %q", i, reason)
			}
			observeRound(&p, succeeding("fs-list", fmt.Sprintf(`{"path":"d%d"}`, i)))
		} else if !strings.Contains(reason, `the call mcp-call {"tool":"x"} failed in 6 of the last 16`) {
			t.Fatalf("failure %d: %q", i, reason)
		}
	}
}

// Waiting for a command that is still running is not repeating a call: the
// same poll four times ended two turns that had done nothing wrong
// (20261003T002506Z-f564a28c, 20261003T143916Z-48f31857). It has its own
// ceiling, so a command that hangs still ends the turn, and any other call
// between the polls starts the count again.
func TestProgressPollsOfARunningCommandHaveTheirOwnCeiling(t *testing.T) {
	var p turnProgress
	poll := call("shell-read", `{"id":"p1","wait":60}`)
	for i := 1; i < pollLimit; i++ {
		if reason := p.observe(poll, toolOutcome{waiting: true}); reason != "" {
			t.Fatalf("poll %d: %q", i, reason)
		}
	}
	if reason := p.observe(poll, toolOutcome{waiting: true}); !strings.Contains(reason, "still running") {
		t.Fatalf("poll %d: %q", pollLimit, reason)
	}

	// A read of the same process after it exited is an ordinary call: four
	// in a row are still a loop.
	p = turnProgress{}
	for i := 0; i < 4; i++ {
		reason := p.observe(poll, toolOutcome{})
		if (reason != "") != (i == 3) {
			t.Fatalf("read %d of an exited process: %q", i, reason)
		}
	}

	// Another call between the polls starts the count again.
	p = turnProgress{}
	for i := 0; i < 3*pollLimit; i++ {
		if i%(pollLimit-1) == pollLimit-2 {
			p.observe(call("fs-read", fmt.Sprintf(`{"path":"a%d"}`, i)), toolOutcome{})
			continue
		}
		if reason := p.observe(poll, toolOutcome{waiting: true}); reason != "" {
			t.Fatalf("poll at %d: %q", i, reason)
		}
	}
}

// The same key pressed several times in a row moves down a file: it is not
// a loop, but a turn that does nothing else for screenLimit calls is.
func TestProgressDrivingATerminalIsNotRepeating(t *testing.T) {
	var p turnProgress
	down := call("term-keys", `{"id":"t1","keys":["Down"]}`)
	for i := 1; i < screenLimit; i++ {
		if reason := p.observe(down, toolOutcome{terminal: true}); reason != "" {
			t.Fatalf("key %d: %q", i, reason)
		}
	}
	if reason := p.observe(down, toolOutcome{terminal: true}); !strings.Contains(reason, "terminal was driven") {
		t.Fatalf("call %d: %q", screenLimit, reason)
	}

	// Without the outcome the same calls are a loop, as before.
	p = turnProgress{}
	for i := 0; i < 4; i++ {
		reason := p.observe(down, toolOutcome{})
		if (reason != "") != (i == 3) {
			t.Fatalf("plain call %d: %q", i, reason)
		}
	}

	// Another tool between them starts the count again, and a key the
	// terminal refused counts as the failure it is.
	p = turnProgress{}
	for i := 0; i < 3*screenLimit; i++ {
		if i%(screenLimit-1) == screenLimit-2 {
			p.observe(call("fs-read", fmt.Sprintf(`{"path":"a%d"}`, i)), toolOutcome{})
			continue
		}
		if reason := p.observe(down, toolOutcome{terminal: true}); reason != "" {
			t.Fatalf("call %d: %q", i, reason)
		}
	}
	p = turnProgress{}
	bad := call("term-keys", `{"id":"t1","keys":["Bogus"]}`)
	for i := 0; i < 4; i++ {
		reason := p.observe(bad, toolOutcome{terminal: true, failed: true})
		if (reason != "") != (i == 3) {
			t.Fatalf("refused key %d: %q", i, reason)
		}
	}
}

func TestStillRunningReadsShellReadsFirstLine(t *testing.T) {
	for answer, want := range map[string]bool{
		"p1: running (bash -c go test ./...)\nok  \tpkg\n": true,
		"p1: running (scripts/promote.sh)\n":               true,
		"p1: exited 0 (scripts/promote.sh)\nselfcheck: ok": false,
		"p1: exited 1\nrunning (not the state)":            false,
		"":                                                 false,
	} {
		if got := stillRunning(answer); got != want {
			t.Errorf("%q: %v, want %v", answer, got, want)
		}
	}
}
