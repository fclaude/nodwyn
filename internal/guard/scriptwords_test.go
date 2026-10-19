package guard_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// argvVerdict is the argv trigger's answer for argv in repo.
func argvVerdict(repo string, argv ...string) guard.Decision {
	return guard.DefaultArgvTrigger(repo).Check(scheme.WithOrigin(context.Background(), scheme.OriginModel), shellP(), shellArgs(argv[0], argv[1:]...))
}

// A shell's -c line is read word by word, whatever it holds: an assignment
// naming the repository before a continued line is no outside path, as it
// was when a line that shellLine would not split was one word ("names a
// path outside the repository: /testbed python \", 48 of the 223 refusals
// on the long-context benchmark, ADR 17, amended 2026-10-10); and a path in
// a substitution, a redirection's target or after a variable is one, which
// that one word hid.
func TestArgvTriggerReadsAScriptWordByWord(t *testing.T) {
	repo, outside := t.TempDir(), t.TempDir()
	for _, line := range []string{
		"PYTHONPATH=" + repo + " python \\\n  -c 'import sys; print(sys.path)'",
		"cd " + repo + " && PYTHONPATH=" + repo + " python -m pytest -q $ARGS 2>&1 | tail -n 20",
		"cat > $TMPDIR/x.py <<'EOF'\nprint(open('" + filepath.Join(outside, "data") + "').read())\nEOF\npython $TMPDIR/x.py",
		"python -c \"import os; print(os.getcwd())\" > /dev/null 2>&1",
	} {
		if d := argvVerdict(repo, "bash", "-c", line); d.Verdict != guard.Allow {
			t.Errorf("%q asked: %+v", line, d)
		}
	}
	target := filepath.Join(outside, "out.txt")
	for _, line := range []string{
		"echo hi > " + target,
		"cat $(ls " + outside + ")",
		"X=1; cat \"$X\" " + target,
		"cat <<EOF > " + target + "\nhello\nEOF",
		"(cd " + outside + " && ls)",
	} {
		d := argvVerdict(repo, "bash", "-c", line)
		if d.Verdict != guard.Confirm || !d.Security || !strings.Contains(d.Reason, outside) {
			t.Errorf("%q was not a security path confirm: %+v", line, d)
		}
	}
	// The home directory spelled as the shell spells it is outside too.
	for _, line := range []string{"cat ~/.aws/credentials", "cat $HOME/.aws/credentials", "cat ${HOME}/.netrc", "cd ~ && cat .aws/credentials", "cat ~root/.profile", "cp x --target-directory=~/bin"} {
		if d := argvVerdict(repo, "bash", "-c", line); d.Verdict != guard.Confirm || !d.Security {
			t.Errorf("%q was not a security path confirm: %+v", line, d)
		}
	}
	// $TMPDIR is the session's scratch directory, which stays unasked.
	trigger := guard.DefaultArgvTrigger(repo)
	trigger.Scratch = t.TempDir()
	if d := trigger.Check(scheme.WithOrigin(context.Background(), scheme.OriginModel), shellP(), shellArgs("bash", "-c", "python $TMPDIR/x.py > ${TMPDIR}/out.txt")); d.Verdict != guard.Allow {
		t.Errorf("$TMPDIR asked: %+v", d)
	}
	// A line it cannot split is read as before, as one word.
	if d := argvVerdict(repo, "bash", "-c", outside+" 'unclosed"); d.Verdict != guard.Confirm {
		t.Errorf("an unsplittable line naming an outside path was not asked about: %+v", d)
	}
}

// A program in a Python environment's bin folder runs by its full path
// without the question, as one on PATH does: the system prompt tells the
// model to find the project's environment and use its interpreter, and
// naming it was 98 of the 223 refusals on the long-context benchmark (ADR
// 17, amended 2026-10-10). Its arguments are still read, a command that
// writes over it still asks, and a folder that is not an environment, or
// one others may write, gets the question.
func TestArgvTriggerLetsAnEnvironmentsInterpreterRun(t *testing.T) {
	repo, base := t.TempDir(), t.TempDir()
	env := func(name, marker string) string {
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		switch marker {
		case "conda-meta":
			if err := os.Mkdir(filepath.Join(dir, "conda-meta"), 0o755); err != nil {
				t.Fatal(err)
			}
		case "pyvenv.cfg":
			if err := os.WriteFile(filepath.Join(dir, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		python := filepath.Join(dir, "bin", "python")
		if err := os.WriteFile(python, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return python
	}
	conda, venv, plain := env("testbed", "conda-meta"), env("venv", "pyvenv.cfg"), env("plain", "")
	open := env("open", "conda-meta")
	if err := os.Chmod(filepath.Dir(open), 0o777); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{conda, "-c", "import django"},
		{venv, "-m", "pytest", "tests"},
		{"bash", "-c", "cd " + repo + " && " + conda + " -m pytest -q 2>&1 | tail -n 5"},
		{"timeout", "60", conda, "runtests.py"},
	} {
		if d := argvVerdict(repo, argv...); d.Verdict != guard.Allow {
			t.Errorf("%q asked: %+v", argv, d)
		}
	}
	for _, argv := range [][]string{
		{plain, "-c", "1"},
		{open, "-c", "1"},
		{"cp", "x.py", conda},
		{conda, filepath.Join(base, "x.py")},
	} {
		if d := argvVerdict(repo, argv...); d.Verdict != guard.Confirm || !d.Security {
			t.Errorf("%q was not a security path confirm: %+v", argv, d)
		}
	}
}

// A sed or awk script, a grep or rg pattern and git log's -L range are not
// files, though a sed address such as /^def polyval/,/^def /p looks like an
// absolute path; the files they read still are.
func TestArgvTriggerReadsPatternsAsPatterns(t *testing.T) {
	repo, outside := t.TempDir(), t.TempDir()
	for _, argv := range [][]string{
		{"sed", "-n", "/^def polyval/,/^def /p", "xarray/core/computation.py"},
		{"bash", "-c", "sed -n '/^def polyval/,/^def /p' xarray/core/computation.py | head -n 40"},
		{"sed", "-n", "-e", "/^class /p", "-e", "/^def /p", "a.py"},
		{"grep", "-rn", "/usr/lib", "."},
		{"rg", "-n", "-e", "/etc/hosts", "src"},
		{"awk", "/^def /{print NR}", "a.py"},
		{"git", "log", "-L", "/def get_group_by_cols/,+6:django/db/models/lookups.py"},
		{"git", "grep", "-n", "/opt/conda", "--", "*.py"},
	} {
		if d := argvVerdict(repo, argv...); d.Verdict != guard.Allow {
			t.Errorf("%q asked: %+v", argv, d)
		}
	}
	file := filepath.Join(outside, "f")
	for _, argv := range [][]string{
		{"sed", "-n", "1p", file},
		{"sed", "-f", file, "a.py"},
		{"grep", "-f", file, "a.py"},
		{"grep", "-rn", "x", file},
		{"awk", "-f", file, "a.py"},
	} {
		if d := argvVerdict(repo, argv...); d.Verdict != guard.Confirm || !strings.Contains(d.Reason, outside) {
			t.Errorf("%q was not asked about: %+v", argv, d)
		}
	}
}

// What a command does with a path decides whether it needs the question
// (ADR 17, amended 2026-10-10: what a command does with a path). Listing
// names, a for loop's list, reading or sourcing what is in a Python
// environment, an environment's paths in an assignment and the scratch
// directory written with a trailing slash need none: on the long-context
// benchmark they were most of the 442 commands the word-by-word reading
// asked about. Reading contents outside an environment, listing inside a
// substitution whose output another command takes, find's actions, sort's
// output file, sourcing a script from elsewhere, writing into an
// environment, running a program from outside and a /tmp path still ask,
// the last with where a throwaway file goes.
func TestArgvTriggerAsksWhatACommandDoesWithAPath(t *testing.T) {
	repo, base, outside := t.TempDir(), t.TempDir(), t.TempDir()
	conda := filepath.Join(base, "miniconda3")
	testbed := filepath.Join(conda, "envs", "testbed")
	for _, dir := range []string{filepath.Join(conda, "conda-meta"), filepath.Join(conda, "etc", "profile.d"),
		filepath.Join(testbed, "conda-meta"), filepath.Join(testbed, "bin"), filepath.Join(testbed, "lib", "site-packages")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, mode := range map[string]os.FileMode{
		filepath.Join(conda, "etc", "profile.d", "conda.sh"):   0o644,
		filepath.Join(testbed, "bin", "python"):                0o755,
		filepath.Join(testbed, "lib", "site-packages", "x.py"): 0o644,
		filepath.Join(outside, "evil.sh"):                      0o644,
	} {
		if err := os.WriteFile(path, []byte("#\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	scratch := filepath.Join(base, "scratch")
	if err := os.Mkdir(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	trigger := guard.DefaultArgvTrigger(repo)
	trigger.Scratch = scratch
	verdict := func(argv ...string) guard.Decision {
		return trigger.Check(scheme.WithOrigin(context.Background(), scheme.OriginModel), shellP(), shellArgs(argv[0], argv[1:]...))
	}
	python := filepath.Join(testbed, "bin", "python")
	for _, argv := range [][]string{
		{"ls", "-la", outside},
		{"bash", "-c", "ls -d " + base + "/*/envs 2>/dev/null; find / -maxdepth 1 -name python3.6 2>/dev/null | head -n 3"},
		{"bash", "-c", "for p in /usr/bin/python3 " + python + "; do echo -n \"$p: \"; $p -V; done"},
		{"bash", "-c", "source " + filepath.Join(conda, "etc", "profile.d", "conda.sh") + "; conda activate testbed; python -V"},
		{"bash", "-c", "PY=$(ls -d " + base + "/*/envs/*/bin/python | head -1); \"$PY\" -V"},
		{"bash", "-c", "P=" + python + "; \"$P\" --version"},
		{"bash", "-c", "PYTHONPATH=" + repo + ":" + repo + "/tests PATH=" + filepath.Join(testbed, "bin") + ":$PATH python -m pytest -q"},
		{"cat", filepath.Join(testbed, "lib", "site-packages", "x.py")},
		{"ls", scratch + "/"},
	} {
		if d := verdict(argv...); d.Verdict != guard.Allow {
			t.Errorf("%q asked: %+v", argv, d)
		}
	}
	for _, argv := range [][]string{
		{"cat", filepath.Join(outside, "evil.sh")},
		{"bash", "-c", "cat $(ls -d " + outside + "/*)"},
		{"find", outside, "-name", "*.sh", "-exec", "cat", "{}", "+"},
		{"sort", "-o", filepath.Join(outside, "out"), "x.txt"},
		{"bash", "-c", "source " + filepath.Join(outside, "evil.sh")},
		{"cp", "x.py", filepath.Join(testbed, "lib", "site-packages", "x.py")},
		{filepath.Join(outside, "ls"), repo},
		{"python3", filepath.Join(outside, "evil.sh")},
	} {
		if d := verdict(argv...); d.Verdict != guard.Confirm || !d.Security {
			t.Errorf("%q was not a security path confirm: %+v", argv, d)
		}
	}
	if d := verdict("python3", "/tmp/check.py"); d.Verdict != guard.Confirm || !strings.Contains(d.Reason, "written as $TMPDIR/check.py") {
		t.Errorf("a /tmp path's refusal does not say where a throwaway file goes: %+v", d)
	}
}

// What echo and printf print and what a for loop lists are no file access
// until another command takes them for its arguments (ADR 17, amended
// 2026-10-19). A path outside the repository is a security confirm that no
// standing approval, judge or unattended setting answers, and these forms
// read it all the same: cat $(echo /outside), echo /outside | xargs cat, a
// for loop over /outside/* with cat $f in its body, find /outside | xargs
// cat. The listings and prints that stay unasked are the ones nothing reads:
// echo and printf by themselves, a for loop over the repository's files or
// over interpreters, ls and find piped to a filter.
func TestArgvTriggerReadsWhatFeedsAnotherCommand(t *testing.T) {
	repo, outside, base := t.TempDir(), t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conda := filepath.Join(base, "miniconda3")
	python := filepath.Join(conda, "envs", "testbed", "bin", "python")
	for _, dir := range []string{filepath.Join(conda, "conda-meta"), filepath.Join(conda, "envs", "testbed", "conda-meta"), filepath.Dir(python)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(python, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"cat $(echo " + secret + ")",
		"cat $(printf %s " + secret + ")",
		"cat `echo " + secret + "`",
		"cat \"$(echo " + secret + ")\"",
		"cat \"$(ls " + outside + ")\"",
		"cat \"`echo " + secret + "`\"",
		"echo \"x: $(printf %s \"" + secret + "\") y\"| xargs",
		"cat \"$(cat \"$(echo " + secret + ")\")\"",
		"X=$(echo " + secret + "); cat \"$X\"",
		"echo " + secret + " | xargs cat",
		"printf '%s\\n' " + secret + " | xargs cat",
		"echo " + secret + " |& xargs cat",
		"echo " + secret + " | while read f; do cat \"$f\"; done",
		"(echo " + secret + ") | xargs cat",
		"{ echo " + secret + "; } | xargs cat",
		"true && echo " + secret + " | sh",
		"for f in " + outside + "/*; do cat $f; done",
		"for f in " + secret + "; do cat \"$f\"; done",
		"for f in " + outside + "/*; do echo $f; done | xargs cat",
		"find " + outside + " | xargs cat",
		"ls -d " + outside + "/* | xargs cat",
		"find " + outside + " -type f | while read f; do cat \"$f\"; done",
		"echo hi >&" + secret,
		"echo hi >& " + secret,
		"echo hi 2>&" + secret,
		"echo hi >>&" + secret,
	} {
		d := argvVerdict(repo, "bash", "-c", line)
		if d.Verdict != guard.Confirm || !d.Security || !strings.Contains(d.Reason, outside) {
			t.Errorf("%q was not a security path confirm: %+v", line, d)
		}
	}
	// Where nobody could approve the path (a lean session nobody approves,
	// which may use /tmp) it is refused outright, as any outside path is.
	if temp := guard.TempDir(); temp != "" {
		trigger := guard.DefaultArgvTrigger(repo)
		trigger.Temp = temp
		for _, line := range []string{"cat $(echo " + secret + ")", "echo " + secret + " | xargs cat", "for f in " + secret + "; do cat $f; done", "echo hi >&" + secret} {
			if d := trigger.Check(scheme.WithOrigin(context.Background(), scheme.OriginModel), shellP(), shellArgs("bash", "-c", line)); d.Verdict != guard.Deny {
				t.Errorf("%q was not refused where nobody approves: %+v", line, d)
			}
		}
	}
	for _, line := range []string{
		"echo " + secret,
		"printf '%s\\n' " + secret + " > notes.txt",
		"echo hello | tr a-z A-Z",
		"echo " + python + " | head -n 1",
		"cat $(echo " + python + ")",
		"echo \"$(date) in $(pwd)\"",
		"P=\"$(ls -d " + conda + "/envs/*/bin/python | head -1)\"; \"$P\" -V",
		"cat \"$(git rev-parse --show-toplevel)/src/a.go\"",
		"echo \"a (b) $(echo 'c)')\"",
		"for f in src/*.go; do cat $f; done",
		"for p in /usr/bin/python3 " + python + "; do echo -n \"$p: \"; $p -V; done",
		"for p in " + conda + "/envs/*/bin/python; do $p -V; done",
		"ls " + outside + " | head -n 3",
		"find " + outside + " -name 'sec*' 2>/dev/null | sort | head",
		"ls -d " + outside + "/* | wc -l",
		"echo hi >&2",
		"python -c 'print(1)' 2>&1 | tail -n 5",
		"python -c 'print(1)' >&2",
		"python -c 'print(1)' >&- 2>&-",
		"echo hi >& notes.txt",
	} {
		if d := argvVerdict(repo, "bash", "-c", line); d.Verdict != guard.Allow {
			t.Errorf("%q asked: %+v", line, d)
		}
	}
}

// The network tools and the hard-trigger patterns are looked for in the
// commands a -c line holds, as they are in an argv: bash -c "curl ..." named
// no network tool and "git  push" matched no pattern when the line was read
// as one word, or as a string with its spaces as they were typed.
func TestArgvTriggerFindsNetworkToolsAndPatternsInAScript(t *testing.T) {
	repo := t.TempDir()
	for _, line := range []string{
		"curl https://example.com",
		"cd src && curl -s https://example.com | head",
		"echo $(wget -qO- https://example.com)",
		"env FOO=1 ssh host true",
		"timeout 5 nc -z host 22",
		"git  push origin main",
		"git\tpush",
		"echo ok; git push",
		"sudo  rm -rf build",
	} {
		if d := argvVerdict(repo, "bash", "-c", line); d.Verdict != guard.Confirm || d.Security {
			t.Errorf("%q was not a plain confirm: %+v", line, d)
		}
	}
	// A tool named in text is no command: a pattern, a message, an argument.
	for _, line := range []string{
		"grep -rn curl src",
		"echo 'use curl here'",
		"git commit -m 'push it'",
		"ls curl",
	} {
		if d := argvVerdict(repo, "bash", "-c", line); d.Verdict != guard.Allow {
			t.Errorf("%q asked: %+v", line, d)
		}
	}
}

// A shell that a command line starts is read as the top-level one is (ADR 17,
// amended 2026-10-19, nested shells): the string after its -c, and what eval
// joins, are command lines of their own. As one word they read as a path
// relative to the repository, so `sh -c 'cat /etc/hosts'` inside a -c line,
// or behind env, sudo, xargs or find -exec, asked nobody about /etc/hosts,
// though the same line outside a nested shell is a security confirm.
func TestArgvTriggerReadsANestedShellsScript(t *testing.T) {
	repo, outside, base := t.TempDir(), t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret")
	python := filepath.Join(base, "envs", "testbed", "bin", "python")
	for _, dir := range []string{filepath.Join(base, "envs", "testbed", "conda-meta"), filepath.Dir(python)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(python, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"sh", "-c", "cat " + secret, "name"},
		{"env", "sh", "-c", "cat " + secret},
		{"timeout", "5", "bash", "-lc", "cat " + secret},
		{"sudo", "sh", "-c", "cat " + secret},
		{"xargs", "sh", "-c", "cat " + secret, "_"},
		{"find", ".", "-exec", "sh", "-c", "cat " + secret, ";"},
		{"bash", "-c", "sh -c 'cat " + secret + "'"},
		{"bash", "-c", "bash -ec \"cat " + secret + "\""},
		{"bash", "-c", "bash -c \"sh -c 'cat " + secret + "'\""},
		{"bash", "-c", "sh -o pipefail -c 'cat " + secret + "'"},
		{"bash", "-c", "eval \"cat " + secret + "\""},
		{"bash", "-c", "eval cat " + secret},
		{"bash", "-c", "eval 'echo hi; cat " + secret + "'"},
		{"bash", "-c", "cd src && eval \"$(echo cat " + secret + ")\""},
		{"bash", "-c", "cat $(sh -c 'echo " + secret + "')"},
		{"bash", "-c", "sh -c 'echo " + secret + "' | xargs cat"},
		{"bash", "-c", "sh -c 'cat \"$(echo " + secret + ")\"'"},
		{"bash", "-c", "sh -c 'echo hi >&" + secret + "'"},
		{"bash", "-c", "sh -c 'for f in " + outside + "/*; do cat $f; done'"},
	} {
		d := argvVerdict(repo, argv...)
		if d.Verdict != guard.Confirm || !d.Security || !strings.Contains(d.Reason, outside) {
			t.Errorf("%q was not a security path confirm: %+v", argv, d)
		}
	}
	// The network tools and hard-trigger patterns in a nested script are found.
	for _, line := range []string{"sh -c 'curl https://example.com'", "bash -c \"sh -c 'git  push'\"", "eval 'wget -qO- https://example.com'"} {
		if d := argvVerdict(repo, "bash", "-c", line); d.Verdict != guard.Confirm || d.Security {
			t.Errorf("%q was not a plain confirm: %+v", line, d)
		}
	}
	// Where nobody could approve the path it is refused outright.
	if temp := guard.TempDir(); temp != "" {
		trigger := guard.DefaultArgvTrigger(repo)
		trigger.Temp = temp
		for _, line := range []string{"sh -c 'cat " + secret + "'", "eval cat " + secret} {
			if d := trigger.Check(scheme.WithOrigin(context.Background(), scheme.OriginModel), shellP(), shellArgs("bash", "-c", line)); d.Verdict != guard.Deny {
				t.Errorf("%q was not refused where nobody approves: %+v", line, d)
			}
		}
	}
	// What a nested script spells inside the repository, or the environment's
	// interpreter, is unasked, and so is a shell that runs a file or reads
	// nothing: its -c may be another program's flag.
	for _, argv := range [][]string{
		{"sh", "-c", "cat a.go"},
		{"bash", "-c", "sh -c 'python -m pytest -q 2>&1 | tail -n 5'"},
		{"bash", "-c", "sh -c 'cd " + repo + " && PYTHONPATH=" + repo + " python -V'"},
		{"bash", "-c", "bash -lc '" + python + " -V'"},
		{"bash", "-c", "sh -c '/usr/bin/python3 -V; echo hi'"},
		{"bash", "-c", "eval \"$(date)\""},
		{"bash", "-c", "eval echo " + secret},
		{"bash", "-c", "sh -c 'echo " + secret + "'"},
		{"bash", "-c", "sh -c 'ls " + outside + "'"},
		{"bash", "-c", "sh build.sh -c " + secret + ".conf" + " >/dev/null"},
		{"bash", "-c", "grep -c bash a.go"},
		{"bash", "-c", "sh -c \"cat 'unclosed\""},
		{"sh", "-c", "git status --short"},
	} {
		d := argvVerdict(repo, argv...)
		// The two that name the outside path are asked about by their own
		// words, not their script: echo prints it, ls lists it.
		if strings.Contains(strings.Join(argv, " "), "build.sh") {
			if d.Verdict != guard.Confirm || !strings.Contains(d.Reason, outside) {
				t.Errorf("%q: a word that is no script was not judged as a path: %+v", argv, d)
			}
			continue
		}
		if d.Verdict != guard.Allow {
			t.Errorf("%q asked: %+v", argv, d)
		}
	}
}
