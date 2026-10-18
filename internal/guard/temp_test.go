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

// A lean session nobody approves may use /tmp as it uses its scratch
// directory (ADR 32, amended 2026-10-17): the argv trigger lets a command
// write and read there, and refuses outright, saying that nothing ran, a path
// outside the repository, $TMPDIR and /tmp. /tmp is judged with its links
// resolved, and neither another session's scratch directory in it, nor a
// credential's name, nor a link out of it, nor a parent component is allowed.
func TestALeanUnattendedSessionMayUseTmp(t *testing.T) {
	temp := guard.TempDir()
	if temp == "" {
		t.Skip("no /tmp")
	}
	if real, _ := filepath.EvalSymlinks("/tmp"); temp != real {
		t.Fatalf("TempDir %q, want /tmp resolved, %q", temp, real)
	}
	repo := t.TempDir()
	scratch := filepath.Join(temp, "nodwyn-scratch-me-"+filepath.Base(t.TempDir()))
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(scratch) })
	link := filepath.Join(temp, "nodwyn-guard-test-link-"+filepath.Base(t.TempDir()))
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(link) })
	trigger := guard.DefaultArgvTrigger(repo)
	trigger.Scratch, trigger.Temp = scratch, temp
	ctx := scheme.WithOrigin(context.Background(), scheme.OriginModel)
	verdict := func(t guard.ArgvTrigger, argv ...string) guard.Decision {
		return t.Check(ctx, shellP(), shellArgs(argv[0], argv[1:]...))
	}
	for _, line := range []string{
		"cat > /tmp/repro.py <<'EOF'\nprint(1)\nEOF\npython /tmp/repro.py",
		"python3 /tmp/repro.py > /tmp/out.txt 2>&1; cat /tmp/out.txt",
		"mkdir -p /tmp/work && cp main.py /tmp/work/ && ls /tmp/work",
		"echo x > " + filepath.Join(temp, "real-path.txt"),
		"echo x > $TMPDIR/x && cat " + scratch + "/x",
	} {
		if d := verdict(trigger, "bash", "-c", line); d.Verdict != guard.Allow {
			t.Errorf("%q: %+v", line, d)
		}
	}
	if d := verdict(trigger, "python3", "/tmp/repro.py"); d.Verdict != guard.Allow {
		t.Errorf("a program given a /tmp file: %+v", d)
	}
	for _, line := range []string{
		"cat /etc/hosts",
		"echo x > /tmp/../etc/x",
		"cat /tmp/id_rsa",
		"cp /tmp/server.pem .",
		"cat /tmp/nodwyn-scratch-other-1234/notes.txt",
		"cat " + link + "/hosts",
		"echo x > /var/x",
	} {
		d := verdict(trigger, "bash", "-c", line)
		if d.Verdict != guard.Deny || !strings.Contains(d.Reason, "outside the repository, $TMPDIR and /tmp, so it was refused and nothing of it ran") {
			t.Errorf("%q: %+v", line, d)
		}
	}
	// Without it, as in a full session or one a person approves, /tmp is
	// outside, and a person is asked about it.
	plain := guard.DefaultArgvTrigger(repo)
	plain.Scratch = scratch
	if d := verdict(plain, "bash", "-c", "python /tmp/repro.py"); d.Verdict != guard.Confirm || !d.Security || !strings.Contains(d.Reason, "names a path outside the repository: /tmp/repro.py") {
		t.Errorf("without /tmp: %+v", d)
	}
}

// The repository pin lets a file tool use /tmp in the same session, and its
// refusal of anything else outside says nothing was done.
func TestTheRepoPinAllowsTmpWhenItIsGiven(t *testing.T) {
	temp := guard.TempDir()
	if temp == "" {
		t.Skip("no /tmp")
	}
	repo := t.TempDir()
	pin := guard.RepoPin{Root: repo, Temp: temp}
	if pin.Resolve("/tmp/repro.py") == "" {
		t.Error("/tmp/repro.py is not inside")
	}
	if pin.Resolve("/tmp/id_rsa") != "" || pin.Resolve("/etc/hosts") != "" || pin.Resolve("/tmp/../etc/hosts") != "" {
		t.Error("a credential's name, or a path outside /tmp, is inside")
	}
	ctx := scheme.WithOrigin(context.Background(), scheme.OriginModel)
	if d := pin.Check(ctx, writeP(), []scheme.Value{scheme.Str("/tmp/repro.py"), scheme.Str("x")}); d.Verdict != guard.Allow {
		t.Errorf("a write to /tmp: %+v", d)
	}
	if d := pin.Check(ctx, writeP(), []scheme.Value{scheme.Str("/etc/x"), scheme.Str("x")}); d.Verdict != guard.Deny || !strings.Contains(d.Reason, "outside the repository, $TMPDIR and /tmp, so the call was refused and nothing was done") {
		t.Errorf("a write outside: %+v", d)
	}
	if (guard.RepoPin{Root: repo}).Resolve("/tmp/repro.py") != "" {
		t.Error("without Temp, /tmp is inside")
	}
}

// A command-line word that is a glob pattern is judged by where it can match:
// its leading directory and every path it matches now, each as that path
// written out would be (ADR 32, amended 2026-10-17). Read as written, the
// pattern's own components were judged as names: on commit-bench (task
// 6e18b7ba, 2026-10-17) a lean session whose $TMPDIR was
// /tmp/nodwyn-scratch-20261018T030629Z-60251800-878901457 was refused the
// line below, its pattern taken for another session's scratch directory,
// while a link in /tmp read through a pattern was not judged by where it
// points.
func TestAGlobIsJudgedByWhereItCanMatch(t *testing.T) {
	temp := guard.TempDir()
	if temp == "" {
		t.Skip("no /tmp")
	}
	repo := t.TempDir()
	scratch := filepath.Join(temp, "nodwyn-scratch-20261018T030629Z-60251800-"+filepath.Base(t.TempDir()))
	if err := os.MkdirAll(filepath.Join(scratch, "nodwyn-validate-1234"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(scratch) })
	const line = `sleep 110; ls /tmp/vd3.done 2>/dev/null || echo RUNNING; D=$(ls -td /tmp/nodwyn-scratch-*/nodwyn-validate-* 2>/dev/null | head -n1); echo "$D"`
	// Another session's directory that the pattern matches makes it reach
	// outside: then the refusal is right, and says nothing of the fix.
	matches, _ := filepath.Glob("/tmp/nodwyn-scratch-*/nodwyn-validate-*")
	for _, m := range matches {
		if real, _ := filepath.EvalSymlinks(m); !strings.HasPrefix(real, scratch+"/") {
			t.Skipf("%s is another session's: the pattern reaches it", m)
		}
	}
	trigger := guard.DefaultArgvTrigger(repo)
	trigger.Scratch, trigger.Temp = scratch, temp
	ctx := scheme.WithOrigin(context.Background(), scheme.OriginModel)
	verdict := func(t guard.ArgvTrigger, argv ...string) guard.Decision {
		return t.Check(ctx, shellP(), shellArgs(argv[0], argv[1:]...))
	}
	if d := verdict(trigger, "bash", "-c", line); d.Verdict != guard.Allow {
		t.Errorf("the line commit-bench's lean session was refused: %+v", d)
	}
	// A link in /tmp to a directory outside, reached through a pattern.
	link := filepath.Join(temp, "nodwyn-guard-glob-"+filepath.Base(t.TempDir()))
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(link) })
	other := filepath.Join(temp, "nodwyn-scratch-other-"+filepath.Base(t.TempDir()))
	if err := os.MkdirAll(filepath.Join(other, "nodwyn-validate-9"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(other) })
	for _, line := range []string{
		"cat /tmp/../etc/*",
		"cat " + link[:len(link)-2] + "*/hosts",
		"D=$(ls -d " + other[:len(other)-3] + "*/nodwyn-validate-*)",
		"cat /tmp/*.pem",
	} {
		d := verdict(trigger, "bash", "-c", line)
		if d.Verdict != guard.Deny || !strings.Contains(d.Reason, "outside the repository, $TMPDIR and /tmp, so it was refused and nothing of it ran") {
			t.Errorf("%q: %+v", line, d)
		}
	}
	// In the repository a pattern is inside, unless a match is a link out of
	// it; in a session a person approves, a pattern outside asks them.
	if err := os.MkdirAll(filepath.Join(repo, "pkg", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "pkg", "a", "x.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := guard.DefaultArgvTrigger(repo)
	plain.Scratch = scratch
	for _, tr := range []guard.ArgvTrigger{trigger, plain} {
		if d := verdict(tr, "bash", "-c", "cat pkg/*/*.go "+repo+"/pkg/?/x.go $TMPDIR/nodwyn-validate-*"); d.Verdict != guard.Allow {
			t.Errorf("a pattern in the repository and the scratch directory (temp %q): %+v", tr.Temp, d)
		}
	}
	if err := os.Symlink("/etc", filepath.Join(repo, "pkg", "etc")); err != nil {
		t.Fatal(err)
	}
	if d := verdict(trigger, "bash", "-c", "cat pkg/e*/hosts"); d.Verdict != guard.Deny {
		t.Errorf("a pattern in the repository that matches through a link out of it: %+v", d)
	}
	if d := verdict(plain, "bash", "-c", "cat pkg/e*/hosts"); d.Verdict != guard.Confirm || !d.Security {
		t.Errorf("the same in a session a person approves: %+v", d)
	}
	if d := verdict(plain, "bash", "-c", "D=$(ls -d /tmp/nodwyn-scratch-*/nodwyn-validate-*)"); d.Verdict != guard.Confirm || !d.Security {
		t.Errorf("a pattern in /tmp where /tmp is not allowed: %+v", d)
	}
}
