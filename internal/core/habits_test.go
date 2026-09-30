package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A strategy reads the models' and the window's properties (ADR 32).
func TestModelAndWindowPrimitives(t *testing.T) {
	r := strategyRig(t, "", 10)
	for src, want := range map[string]string{
		"(assoc-ref (model-info) 'name)":              `"control"`,
		"(assoc-ref (model-info \"control\") 'name)":  `"control"`,
		"(assoc-ref (model-info) 'max-output)":        "16384",
		"(assoc-ref (window-limits) 'high-water)":     "61440",
		"(assoc-ref (window-limits) 'low-water)":      "30720",
		"(assoc-ref (window-limits) 'output-reserve)": "16384",
		"(car (model-names))":                         `"control"`,
	} {
		if got := r.eval(src); got != want {
			t.Errorf("%s = %s, want %s", src, got, want)
		}
	}
	if _, err := r.s.Eval(context.Background(), `(model-info "nope")`); err == nil || !strings.Contains(err.Error(), `no model "nope"`) {
		t.Fatalf("unknown model: %v", err)
	}
}

// A command line sent through cmd/args is answered, unrun, with the bash -c
// call that runs it (shell.scm).
func TestShellCommandLineIsRedirected(t *testing.T) {
	pipe := fake.ToolCall{ID: "p", Name: "shell-exec", Args: map[string]any{"cmd": "ls", "args": []any{"-1", "|", "head"}}}
	r := strategyRig(t, "", 10, fake.Turn{ToolCalls: []fake.ToolCall{pipe}}, fake.Turn{Content: "done"})
	if err := r.turn("list the files"); err != "" {
		t.Fatal(err)
	}
	raw := string(r.srv.Requests[1].Raw)
	if !strings.Contains(raw, `Not executed: shell-exec runs one program with its arguments`) ||
		!strings.Contains(raw, `call shell-exec with cmd \\\"bash\\\" and args [\\\"-c\\\", \\\"ls -1 | head\\\"]`) {
		t.Fatalf("command line not redirected:\n%s", raw)
	}
}

// read-definition returns a definition by name with its line range
// (readdef.scm).
func TestReadDefinitionByName(t *testing.T) {
	call := fake.ToolCall{ID: "d", Name: "read-definition", Args: map[string]any{"path": "lib.py", "name": "shout"}}
	r := strategyRig(t, "", 10, fake.Turn{ToolCalls: []fake.ToolCall{call}}, fake.Turn{Content: "done"})
	src := "import re\n\n\ndef shout(s):\n    return s.upper()\n\n\ndef whisper(s):\n    return s.lower()\n"
	if err := os.WriteFile(filepath.Join(r.repo, "lib.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.turn("show me shout"); err != "" {
		t.Fatal(err)
	}
	raw := string(r.srv.Requests[1].Raw)
	if !strings.Contains(raw, `[lines 4-5 of lib.py]\ndef shout(s):\n    return s.upper()`) || strings.Contains(raw, "def whisper") {
		t.Fatalf("definition not read by name:\n%s", raw)
	}
}
