package guard

import (
	"strings"
	"testing"
)

// A standing approval names the program a line runs, never the shell that
// runs it: "bash" was offered for every `bash -c` line, and approving it
// approved every command (ADR 17, amended 2026-10-05). A line of one
// command, piped or not into filters, is offered as that command; a line of
// several, or an interpreter, is offered nothing.
func TestPrefixIsTheCommandAShellRuns(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"bash", "-c", "go test ./x"}, "go test"},
		{[]string{"bash", "-c", "go test ./... 2>&1 | tail -n 40"}, "go test"},
		{[]string{"/bin/sh", "-c", "ls"}, "ls"},
		{[]string{"bash", "-c", "go vet ./... && go test ./..."}, ""},
		{[]string{"bash", "-c", "go test; rm -rf x"}, ""},
		{[]string{"bash", "-c", "cd internal && go test"}, ""},
		{[]string{"bash", "-c", "bash -c ls"}, ""},
		{[]string{"bash", "script.sh"}, ""},
		{[]string{"bash"}, ""},
		{[]string{"python3", "-m", "pytest"}, ""},
		{[]string{"env", "GOFLAGS=-x", "go", "test"}, ""},
		{[]string{"go", "test", "./x"}, "go test"},
	} {
		if got := Prefix(tc.argv); got != tc.want {
			t.Errorf("Prefix(%q) = %q, want %q", tc.argv, got, tc.want)
		}
	}
	compound := Prompt{Argv: []string{"bash", "-c", "go vet ./... && go test ./..."}}
	if o := compound.ApprovalOptions(); o.Session || o.Target != "" {
		t.Errorf("a line of several commands offers %+v", o)
	}
	if !compound.Ordinary() {
		t.Error("a line of several commands is not ordinary: don't ask would leave it asking")
	}
	simple := Prompt{Argv: []string{"bash", "-c", "go test ./x | tail -n 5"}}
	if o := simple.ApprovalOptions(); !o.Session || o.Target != "go test" {
		t.Errorf("a line of one command offers %+v", o)
	}
}

// What the person approved covers a -c line only when every command in it is
// approved, or is a filter a pipe feeds that names no file; anything that
// could run, read or write something else asks.
func TestStandingApprovalCoversOnlyApprovedLines(t *testing.T) {
	allow := NewCommandAllow("go test", "go vet", "git status")
	line := func(s string) []string { return []string{"bash", "-c", s} }
	for _, tc := range []struct {
		line string
		want string
	}{
		{"go test ./x", "go test"},
		{"go test ./... 2>&1 | tail -n 20", "go test"},
		{"go vet ./... && go test ./x 2>&1 | grep -v '^ok' | head -50", "go vet, go test"},
		{"go test -run 'TestA|TestB' ./x", "go test"},
		{"git status; go test ./x >/dev/null 2>&1", "git status, go test"},
		{"go test ./x\ngo vet ./x", "go test, go vet"},
	} {
		if got := allow.Match(line(tc.line)); got != tc.want {
			t.Errorf("Match(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
	for _, bad := range []string{
		"go test $(rm -rf ~)",
		"go test `id`",
		"go test ./... > ~/.bashrc",
		"go test ./...>out",
		"go test 2> errs",
		"go test | sh",
		"go test | bash -c 'rm -rf x'",
		"go test | tail -n 5 ~/.ssh/id_ed25519",
		"go test | grep -f ~/.ssh/id_ed25519",
		"go test | grep key ~/.ssh/id_ed25519",
		"go test | sort -o ~/.bashrc",
		"go test && curl https://example.com",
		"go test & rm -rf x",
		"go test || rm -rf x",
		"go test; rm -rf x",
		"go test\nrm -rf x",
		"GOFLAGS=-exec=/tmp/x go test",
		"go test $GOFLAGS",
		"(go test)",
		"{ go test; }",
		"go test <(cat ~/.ssh/id_ed25519)",
		"go test < /etc/passwd",
		"go test 'unclosed",
		"go test |& tail",
		"go test |",
		"| tail",
		"cd /tmp && go test",
		"bash -c 'go test'",
		"rm -rf x",
	} {
		if got := allow.Match(line(bad)); got != "" {
			t.Errorf("Match(%q) = %q, want no match", bad, got)
		}
	}
	// The line's positional parameters could hold anything it expands.
	if got := allow.Match([]string{"bash", "-c", "go test", "x", "y"}); got != "" {
		t.Errorf("a line with positional parameters matched %q", got)
	}
	// A shell's own name approves no -c line, even when a person wrote it
	// into config.toml.
	if got := NewCommandAllow("bash").Match(line("rm -rf x")); got != "" {
		t.Errorf("\"bash\" matched a -c line: %q", got)
	}
	if got := strings.Join(allow.List(), ","); got != "git status,go test,go vet" {
		t.Errorf("list: %s", got)
	}
}
