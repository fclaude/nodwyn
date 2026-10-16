package core

import (
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A project that plans its own way redefines nodwyn-turn, as docs/scheme.md
// tells it to, and must still start each turn with what a turn keeps for
// itself reset (P2-060): the resets used to be wrappers of nodwyn-turn, so a
// context that filled twice with nothing written left every later turn asking
// the model with no tools.
func TestRedefinedTurnStillStartsFresh(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, `
(define (nodwyn-turn request) (agent-turn request))
(set! turn-stopping "stop")
(set! stalled 5)
(set! compact-writes-at 3)
`, 10, fake.Turn{Content: "hello"})
	if err := r.turn("hi"); err != "" {
		t.Fatal(err)
	}
	if got := r.eval(`(list turn-stopping stalled compact-writes-at)`); got != "(#f 0 #f)" {
		t.Fatalf("per-turn state after a turn of a project's own nodwyn-turn: %s", got)
	}
}

// Searching the history before a code edit gets a hint to call second-look
// only in a turn that works toward evidence (P2-064). In a chat that asks
// what changed in the last five commits, the history is the answer.
func TestHistoryHintIsNotGivenInAChat(t *testing.T) {
	t.Parallel()
	history := fake.ToolCall{ID: "h", Name: "shell-exec", Args: map[string]any{"cmd": "git", "args": []any{"log", "-5", "--oneline"}}}
	chat := strategyRig(t, "", 10, fake.Turn{ToolCalls: []fake.ToolCall{history}}, fake.Turn{Content: "five commits"})
	if err := chat.turn("what changed in the last five commits?"); err != "" {
		t.Fatal(err)
	}
	if raw := string(chat.srv.Requests[1].Raw); strings.Contains(raw, "second-look") && strings.Contains(raw, "[hint: searching the history") {
		t.Fatalf("a chat's history search got the benchmark hint:\n%s", raw)
	}

	unattended := unattendedRig(t, fake.Turn{ToolCalls: []fake.ToolCall{history}}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"})
	if err := unattended.turn("find where the regression came from"); err != "" {
		t.Fatal(err)
	}
	if raw := string(unattended.srv.Requests[1].Raw); !strings.Contains(raw, "[hint: searching the history") {
		t.Fatalf("an unattended turn's history search lost the hint:\n%s", raw)
	}
}

// The edit flag the history hint reads is the turn's own: an edit in one turn
// does not switch the hint off for the rest of the session (P2-064).
func TestHistoryHintFlagIsResetEachTurn(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, `(set! checkloop-code-edited #t)`, 10, fake.Turn{Content: "hello"})
	if err := r.turn("hi"); err != "" {
		t.Fatal(err)
	}
	if got := r.eval(`checkloop-code-edited`); got != "#f" {
		t.Fatalf("checkloop-code-edited after a turn: %s", got)
	}
}
