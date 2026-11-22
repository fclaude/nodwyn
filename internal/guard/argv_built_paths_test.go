package guard

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fclaude/brote/scheme"
)

// A path the shell builds without spelling it was judged as written: a
// relative name after a cd as one in the repository, a brace expansion as
// the word with its braces, env -S's string as one word, and the file du
// and find read their list from as a names-only argument. Each now asks a
// person as the same path written out does, and sandbox mode refuses the
// ones in its off-limits directories.
func TestArgvTriggerJudgesPathsTheShellBuilds(t *testing.T) {
	shell := &scheme.Prim{Name: "shell-exec", Meta: scheme.Meta{Doc: "d", Irreversible: true, Params: []scheme.Param{
		{Name: "cmd", Type: "string", Role: "argv"}, {Name: "args", Type: "list", Role: "argv"},
	}}}
	repo := t.TempDir()
	check := func(tr ArgvTrigger, argv ...string) Decision {
		t.Helper()
		var rest []scheme.Value
		for _, w := range argv[1:] {
			rest = append(rest, scheme.Str(w))
		}
		return tr.Check(context.Background(), shell, []scheme.Value{scheme.Str(argv[0]), scheme.List(rest...)})
	}
	tr := DefaultArgvTrigger(repo)
	for _, argv := range [][]string{
		{"bash", "-c", "cd && cat .aws/credentials"},
		{"bash", "-c", "cd; cat .ssh/id_rsa"},
		{"bash", "-c", "cd / && cat etc/shadow"},
		{"bash", "-c", "builtin cd / ; cat etc/shadow"},
		{"bash", "-c", "CDPATH=/ cd etc && cat shadow"},
		{"cat", "{,/}etc/shadow"},
		{"cat", "{/etc/shadow,x}"},
		{"bash", "-c", "cd {,/}etc && cat shadow"},
		{"cat", "/{e..e}tc/shadow"},
		{"env", "-S", "cat /etc/shadow"},
		{"bash", "-c", "env -S 'cat /etc/shadow'"},
		{"env", "--split-string=cat /etc/shadow"},
		{"du", "--files0-from=/etc/shadow"},
		{"find", ".", "-files0-from", "/etc/shadow"},
		{"cat", "{" + strings300() + "}"},
	} {
		if d := check(tr, argv...); d.Verdict != Confirm || !d.Security {
			t.Errorf("%q: %v %v %s", argv, d.Verdict, d.Security, d.Reason)
		}
	}
	// What stays inside is not asked about.
	for _, argv := range [][]string{
		{"bash", "-c", "cd src && cat main.go"},
		{"cat", "{a,b}.go"},
		{"du", "-sh", "."},
		{"env", "-S", "go test ./..."},
	} {
		if d := check(tr, argv...); d.Verdict != Allow {
			t.Errorf("%q: %v %s", argv, d.Verdict, d.Reason)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	sandbox := tr
	sandbox.Sandbox = &PathDenylist{Dirs: []string{filepath.Join(home, ".ssh")}}
	for _, argv := range [][]string{
		{"bash", "-c", "cd && cat .ssh/id_rsa"},
		{"cat", "{,$HOME/}.ssh/id_rsa"},
	} {
		if d := check(sandbox, argv...); d.Verdict != Deny {
			t.Errorf("sandbox %q: %v %s", argv, d.Verdict, d.Reason)
		}
	}
}

// strings300 is a brace group of 300 words, more than are judged one by one.
func strings300() string {
	s := "a"
	for range 299 {
		s += ",a"
	}
	return s
}
