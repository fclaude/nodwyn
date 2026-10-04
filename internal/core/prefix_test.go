package core

import (
	"bytes"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// Each request begins with every message of the one before it, the system
// prompt included, across a plan the model writes and the person's next
// message, so a server's prefix cache reads the conversation so far from
// cache. While the system prompt carried the request and its plan (window
// recipes before v15) both rewrote it: the second message of session
// 20261004T063427Z-97214b45 read none of its 120,906 prompt tokens from
// cache, nor did the request after its plan-write (ADR 9, amended
// 2026-10-04).
func TestEachRequestExtendsTheOneBefore(t *testing.T) {
	t.Parallel()
	r := judgedRig(t, nil, "",
		fake.Turn{ToolCalls: []fake.ToolCall{planWriteCall("w", shapedPlan)}},
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("a", 1, "done", "edited")}},
		fake.Turn{Content: "done"},
		fake.Turn{Content: "main.go is the entry point"})
	if e := r.turn("change main.go"); e != "" {
		t.Fatal(e)
	}
	if e := r.turn("what is main.go?"); e != "" {
		t.Fatal(e)
	}
	reqs := r.srv.Requests
	if len(reqs) != 4 {
		t.Fatalf("%d requests", len(reqs))
	}
	for i := 1; i < len(reqs); i++ {
		before, after := reqs[i-1].Messages, reqs[i].Messages
		if len(after) <= len(before) {
			t.Fatalf("request %d has %d messages, the one before it %d", i, len(after), len(before))
		}
		for j := range before {
			if !bytes.Equal(before[j], after[j]) {
				t.Fatalf("request %d changed message %d of the one before it:\n%s\nbecame\n%s", i, j, before[j], after[j])
			}
		}
	}
	if n := replayAll(t, r.logb); n != 4 {
		t.Fatalf("replayed %d requests", n)
	}
}
