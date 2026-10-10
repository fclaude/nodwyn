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
