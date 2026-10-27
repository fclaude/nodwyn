package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// ADR 43: a model edits a line that holds a credential through its placeholder.
// The credential stays in the file without reaching the model, a request or
// the log, and keeping it asks nothing, since nothing credential-shaped is
// new. Writing a credential the model made up still asks as a security
// confirm, which no standing approval waives.
func TestEditThroughACredentialPlaceholder(t *testing.T) {
	t.Parallel()
	const key = "sk-ant-" + "abcdefgh1234"
	const other = "sk-ant-" + "zzzzzzzz9999"
	const holder = "[REDACTED anthropic-key]"
	r := newRigCalls(t, false, 10, nil,
		toolTurn("1", "fs-read", map[string]any{"path": "fixture.go"}),
		toolTurn("2", "fs-edit", map[string]any{"path": "fixture.go", "old": `body := "` + holder + `"`, "new": `body := "` + holder + `" // fixture`}),
		fake.Turn{Content: "kept"},
		toolTurn("3", "fs-edit", map[string]any{"path": "fixture.go", "old": `body := "` + holder + `" // fixture`, "new": `body := "` + other + `"`}),
		fake.Turn{Content: "replaced"},
	)
	file := filepath.Join(r.repo, "fixture.go")
	if err := os.WriteFile(file, []byte("name := 1\nbody := \""+key+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if asked := r.asked("add a comment to the body line", "y"); len(asked) != 0 {
		t.Fatalf("keeping a credential asked: %+v", asked)
	}
	if b, _ := os.ReadFile(file); string(b) != "name := 1\nbody := \""+key+"\" // fixture\n" {
		t.Fatalf("file after the first edit: %q", b)
	}
	asked := r.asked("use another fixture", "y")
	if len(asked) != 1 || !asked[0].Prompt.Security {
		t.Fatalf("writing a credential did not ask as a security confirm: %+v", asked)
	}
	if b, _ := os.ReadFile(file); string(b) != "name := 1\nbody := \""+other+"\"\n" {
		t.Fatalf("file after the second edit: %q", b)
	}
	for i, req := range r.srv.Requests {
		if strings.Contains(string(req.Raw), key) || strings.Contains(string(req.Raw), other) {
			t.Fatalf("request %d carried a credential", i)
		}
	}
	if !strings.Contains(string(r.srv.Requests[1].Raw), `body := \"`+holder+`\"`) {
		t.Fatal("the model was not shown the placeholder it edits through")
	}
	if log := r.logb.String(); strings.Contains(log, key) || strings.Contains(log, other) {
		t.Fatal("a credential reached the log")
	}
}
