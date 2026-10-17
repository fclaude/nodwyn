package guard

import (
	"reflect"
	"testing"
)

// The commands a -c line runs, read as the shell reads it: a here-document's
// body, a quoted pattern and a redirection's target are no command, nor is
// what a substitution runs; an assignment before a program is not its name.
func TestShellCommandsReadsTheLineAsTheShellDoes(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want []string
	}{
		{[]string{"go", "test", "./..."}, []string{"go test ./..."}},
		{[]string{"bash", "-c", "cd /repo && make validate 2>&1 | tail -n 60; echo \"EXIT=${PIPESTATUS[0]}\""},
			[]string{"cd /repo", "make validate", "tail -n 60", "echo EXIT=${PIPESTATUS[0]}"}},
		{[]string{"bash", "-c", "make validate 2>&1 | grep -E '^(PASS|FAIL|validate:)'"},
			[]string{"make validate", "grep -E ^(PASS|FAIL|validate:)"}},
		{[]string{"bash", "-c", "cat > $TMPDIR/x.go <<'EOF'\npackage main\nfunc main() {}\nEOF\ngo run $TMPDIR/x.go"},
			[]string{"cat", "go run $TMPDIR/x.go"}},
		{[]string{"bash", "-c", "GOTOOLCHAIN=go1.26.9 PATH=\"$(go env GOPATH)/bin:$PATH\" make validate"},
			[]string{"make validate"}},
		{[]string{"bash", "-c", "echo 'unclosed"}, nil},
	} {
		if got := ShellCommands(c.argv); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ShellCommands(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}
