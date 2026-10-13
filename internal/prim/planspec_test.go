package prim

import (
	"context"
	"reflect"
	"testing"
)

// The names a request gives: hyphenated, underscored and dotted words,
// names with a capital inside, spans in backquotes; not links, issue
// numbers or versions. The request of commit-bench task 556d6d12, whose
// session-id a planner dropped.
func TestRequestNamesAreTheNamesARequestGives(t *testing.T) {
	got := requestNames("opencode found the same miss and now sends the headers Codex names its session by, session-id, thread-id and x-client-request-id (anomalyco/opencode#53767). A request whose credential was granted chatgpt.tokens.use.direct now carries the session's cache key as those headers too, unless cache_key is off; see https://example.com/a-b and v1.2.3. `X-Session-Id` stays, and readResponses too.")
	want := []string{"session-id", "thread-id", "x-client-request-id", "chatgpt.tokens.use.direct", "cache_key", "X-Session-Id", "readResponses"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("requestNames = %q, want %q", got, want)
	}
}

// A name is found whole and in any case: session-id is in "Session-Id:" and
// in a quoted "session-id", not in X-Session-Id.
func TestHasNameFindsWholeNames(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{
		{`req.Header.Set("Session-Id", key)`, true},
		{`req.Header.Set("session-id", key)`, true},
		{`req.Header.Set("X-Session-Id", key)`, false},
		{`hreq.Header.Set("session-identity", key)`, false},
		{"session-id\n", true},
	} {
		if got := hasName(c.text, "session-id"); got != c.want {
			t.Errorf("hasName(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// plan-evidence sets a plan's names and paths against the change: a name
// only the change's unchanged lines hold is missing, and so is one only a
// removed line held; a new file counts whole.
func TestPlanEvidenceReadsTheChange(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "calc.py", "def one():\n    return 1  # session-id\n", 0o644)
	write(t, root, "docs.md", "notes\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "code")
	write(t, root, "calc.py", "def one():\n    return 10  # X-Session-Id\n", 0o644)
	write(t, root, "flags.py", "THREAD = 'thread-id'\n", 0o644)
	text, changed, err := changeText(context.Background(), k.o)
	if err != nil {
		t.Fatal(err)
	}
	if hasName(text, "session-id") || !hasName(text, "thread-id") || !hasName(text, "X-Session-Id") {
		t.Errorf("change text:\n%s", text)
	}
	if !changed["calc.py"] || !changed["flags.py"] || changed["docs.md"] {
		t.Errorf("changed paths %v", changed)
	}
}
