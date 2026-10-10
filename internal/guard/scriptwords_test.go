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
// 17, amended 2026-10-10). Its arguments are still read, the same path as
// an argument is still a path, and a folder that is not an environment, or
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
		{"cat", conda},
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
