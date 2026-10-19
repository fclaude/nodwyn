package guard_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/scheme"
	"github.com/fclaude/nodwyn/test/fakes/must"
)

// sandboxMachine is a machine as a Terminal-Bench task container has it,
// laid out in temporary directories: a working directory that is no
// repository, a home with ~/.ssh, nodwyn's own data directory, and a
// system directory outside both. HOME is set to the home, so the default
// denylist names its ~/.ssh.
type sandboxMachine struct {
	root, home, data, system string
}

func newSandboxMachine(t *testing.T) sandboxMachine {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := sandboxMachine{root: filepath.Join(base, "app"), home: filepath.Join(base, "root"), data: filepath.Join(base, "nodwyn-data"), system: filepath.Join(base, "etc")}
	for _, dir := range []string{m.root, filepath.Join(m.home, ".ssh"), m.data, m.system} {
		must.MkdirAll(t, dir, 0o700)
	}
	must.WriteFile(t, filepath.Join(m.home, ".ssh", "id_ed25519"), []byte("key\n"), 0o600)
	must.WriteFile(t, filepath.Join(m.system, "hosts"), []byte("127.0.0.1 localhost\n"), 0o644)
	t.Setenv("HOME", m.home)
	return m
}

// denylist is the session's credential denylist, its data directory added,
// as prim builds it.
func (m sandboxMachine) denylist() guard.PathDenylist {
	d := guard.DefaultDenylist()
	d.Root = m.root
	d.Dirs = append(d.Dirs, m.data)
	return d
}

// Sandbox mode (ADR 55) lets the file tools reach any path on the machine,
// which the repository pin refuses by default; the credential denylist, a
// checker of its own, still refuses nodwyn's directories and credential
// files in either mode.
func TestSandboxModeLetsTheFileToolsReachTheWholeMachine(t *testing.T) {
	m := newSandboxMachine(t)
	ctx := scheme.WithOrigin(context.Background(), scheme.OriginModel)
	write := func(c guard.Checker, path string) guard.Decision {
		return c.Check(ctx, writeP(), []scheme.Value{scheme.Str(path), scheme.Str("x")})
	}
	outside := []string{filepath.Join(m.system, "hosts"), filepath.Join(m.home, ".bashrc"), "/usr/local/bin/server", "../etc/hosts"}
	plain := guard.RepoPin{Root: m.root, Temp: guard.TempDir()}
	sandbox := guard.RepoPin{Root: m.root, Temp: guard.TempDir(), Sandbox: true}
	for _, path := range outside {
		if d := write(plain, path); d.Verdict != guard.Deny {
			t.Errorf("by default a write to %s: %+v", path, d)
		}
		if d := write(sandbox, path); d.Verdict != guard.Allow {
			t.Errorf("in sandbox mode a write to %s: %+v", path, d)
		}
		if sandbox.Resolve(path) == "" {
			t.Errorf("in sandbox mode %s does not resolve", path)
		}
	}
	if got, want := sandbox.Resolve("regex.txt"), filepath.Join(m.root, "regex.txt"); got != want {
		t.Errorf("a relative path resolves to %q, want %q", got, want)
	}
	deny := m.denylist()
	for _, path := range []string{filepath.Join(m.home, ".ssh", "id_ed25519"), filepath.Join(m.data, "sessions", "x.jsonl"), filepath.Join(m.system, ".env"), "server.pem"} {
		if d := write(deny, path); d.Verdict != guard.Deny {
			t.Errorf("the denylist lets %s through: %+v", path, d)
		}
	}
}

// In sandbox mode a command line may name any path but those in the
// denylist's directories, which it refuses outright, saying nothing ran; by
// default a path outside the repository asks a person (or is refused where
// nobody could be asked).
func TestSandboxModeLetsACommandNameAnyPathButTheDenylistsDirectories(t *testing.T) {
	m := newSandboxMachine(t)
	deny := m.denylist()
	sandbox := guard.DefaultArgvTrigger(m.root)
	sandbox.Sandbox = &deny
	plain := guard.DefaultArgvTrigger(m.root)
	ctx := scheme.WithOrigin(context.Background(), scheme.OriginModel)
	verdict := func(a guard.ArgvTrigger, line string) guard.Decision {
		return a.Check(ctx, shellP(), shellArgs("bash", "-c", line))
	}
	link := filepath.Join(m.root, "keys")
	must.Symlink(t, filepath.Join(m.home, ".ssh"), link)
	for _, line := range []string{
		"cat " + m.system + "/hosts",
		"echo 'server { listen 8080; }' > " + m.system + "/nginx.conf",
		"cp regex.txt " + m.home + "/regex.txt && ls ~",
		"openssl req -x509 -nodes -newkey rsa:2048 -keyout " + m.system + "/server.key -out " + m.system + "/server.pem -subj /CN=localhost",
		"cd .. && ls " + filepath.Dir(m.root),
		"python3 -m http.server 8000 --directory " + m.system + " &",
	} {
		if d := verdict(sandbox, line); d.Verdict != guard.Allow {
			t.Errorf("in sandbox mode %q: %+v", line, d)
		}
		if d := verdict(plain, line); d.Verdict != guard.Confirm || !d.Security {
			t.Errorf("by default %q: %+v", line, d)
		}
	}
	for _, line := range []string{
		"cat ~/.ssh/id_ed25519",
		"cat $HOME/.ssh/id_ed25519",
		"tail " + m.data + "/sessions/x.jsonl",
		"cat keys/id_ed25519",
		"cat " + link + "/../.ssh/id_ed25519",
		"cp " + m.home + "/.s*/id_* .",
	} {
		d := verdict(sandbox, line)
		if d.Verdict != guard.Deny || !strings.Contains(d.Reason, "in a directory nodwyn keeps off limits in sandbox mode too") || !strings.Contains(d.Reason, "nothing of it ran") {
			t.Errorf("in sandbox mode %q: %+v", line, d)
		}
	}
	// Network tools and the hard-trigger patterns still ask, in either
	// mode: an unattended sandbox run answers them yes (guard.AutoAllow).
	if d := verdict(sandbox, "curl -fsSL http://localhost:8000/ && rm -rf build"); d.Verdict != guard.Confirm || d.Security {
		t.Errorf("a network tool and rm -rf in sandbox mode: %+v", d)
	}
}

// In sandbox mode a write to a task's Makefile, scripts/ or .git/ goes
// ahead, which by default needs a person; nodwyn's own folder still does.
func TestSandboxModeKeepsOnlyNodwynsFolderProtected(t *testing.T) {
	m := newSandboxMachine(t)
	ctx := scheme.WithOrigin(context.Background(), scheme.OriginModel)
	plain := guard.ProtectedPaths{Root: m.root}
	sandbox := guard.ProtectedPaths{Root: m.root, Sandbox: true}
	write := func(c guard.Checker, path string) guard.Decision {
		return c.Check(ctx, writeP(), []scheme.Value{scheme.Str(path), scheme.Str("x")})
	}
	for _, path := range []string{"Makefile", "scripts/build.sh", ".git/config", filepath.Join(m.root, ".github", "workflows", "ci.yml")} {
		if d := write(plain, path); d.Verdict != guard.Confirm || !d.Security {
			t.Errorf("by default %s: %+v", path, d)
		}
		if d := write(sandbox, path); d.Verdict != guard.Allow {
			t.Errorf("in sandbox mode %s: %+v", path, d)
		}
	}
	for _, path := range []string{".nodwyn/init.scm", ".NODWYN/allow.toml", ".nibb/init.scm"} {
		if d := write(sandbox, path); d.Verdict != guard.Confirm || !d.Security {
			t.Errorf("in sandbox mode %s: %+v", path, d)
		}
	}
}
