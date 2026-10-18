package rpc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/test/fakes/must"
)

// A project's startup files are reviewed and trusted from a console of a
// person signed in with a passkey (ADR 45, amended 2026-10-17): the files
// are shown whole, the grant names the revision shown and is refused if the
// files changed since, trust is the user-owned record `nodwyn trust` writes,
// and a paired device, a folder nodwyn does not work in and a pair too large
// to read whole are refused.
func TestProjectTrustIsReviewedAndGrantedByRevision(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	repo := config.CanonicalPath(t.TempDir())
	must.MkdirAll(t, filepath.Join(repo, ".nodwyn"), 0o755)
	must.WriteFile(t, filepath.Join(repo, ".nodwyn", "init.scm"), []byte("(display \"hello\")\n"), 0o644)
	must.WriteFile(t, filepath.Join(repo, ".nodwyn", "allow.toml"), []byte(""), 0o644)
	read := func(client *client) (trustState, *rpcError) {
		t.Helper()
		var state trustState
		r := client.call("trust.read", map[string]any{"repo": repo})
		if r.Error == nil {
			json.Unmarshal(r.Result, &state)
		}
		return state, r.Error
	}
	if _, rerr := read(c); rerr == nil || !strings.Contains(rerr.Message, "folder nodwyn works in") {
		t.Fatalf("a folder nodwyn has not been told of: %+v", rerr)
	}
	c.ok("workspace.save", map[string]any{"path": repo, "name": "proj", "mounts": []any{}})
	state, rerr := read(c)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !state.Present || state.Trusted || !state.Reviewable || state.Init == nil || *state.Init != "(display \"hello\")\n" || state.Allow == nil || state.Revision == "" {
		t.Fatalf("state: %+v", state)
	}
	// A paired device cannot grant it; one signed in with a passkey, or a
	// terminal's client, can.
	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	if r := phone.call("trust.set", map[string]any{"repo": repo, "revision": state.Revision}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
		t.Fatalf("a paired device: %+v", r.Error)
	}
	laptop := connect(t, path)
	laptop.ok("hello", map[string]any{"protocol": 1, "via": "web:laptop", "passkey": "laptop"})
	// The grant names what was read: files changed since are refused.
	if r := laptop.call("trust.set", map[string]any{"repo": repo, "revision": "0000"}); r.Error == nil || !strings.Contains(r.Error.Message, "review them again") {
		t.Fatalf("another revision: %+v", r.Error)
	}
	if again, _ := read(c); again.Trusted {
		t.Fatal("a refused grant trusted the project")
	}
	var granted trustState
	json.Unmarshal(laptop.ok("trust.set", map[string]any{"repo": repo, "revision": state.Revision}), &granted)
	if !granted.Trusted {
		t.Fatalf("granted: %+v", granted)
	}
	// It is the record the sessions consult.
	files, err := config.ReadProjectFiles(repo)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := config.ProjectTrusted(o.Paths, repo, files); err != nil || !ok {
		t.Fatalf("the record: %v %v", ok, err)
	}
	// A change to a file needs another review.
	must.WriteFile(t, filepath.Join(repo, ".nodwyn", "init.scm"), []byte("(display \"changed\")\n"), 0o644)
	if changed, _ := read(c); changed.Trusted || changed.Revision == state.Revision {
		t.Fatalf("after a change: %+v", changed)
	}
	if r := laptop.call("trust.set", map[string]any{"repo": repo, "revision": state.Revision}); r.Error == nil {
		t.Fatal("a stale revision was granted")
	}
	// Revoking is a grant of nothing.
	var after trustState
	fresh, _ := read(c)
	laptop.ok("trust.set", map[string]any{"repo": repo, "revision": fresh.Revision})
	json.Unmarshal(laptop.ok("trust.set", map[string]any{"repo": repo, "revision": ""}), &after)
	if after.Trusted {
		t.Fatalf("revoked: %+v", after)
	}
	// A pair too large to read whole is not shown, and not offered.
	must.WriteFile(t, filepath.Join(repo, ".nodwyn", "init.scm"), []byte(strings.Repeat("; x\n", trustShown/4+10)), 0o644)
	if big, rerr := read(c); rerr != nil || big.Reviewable || big.Init != nil || big.Revision == "" {
		t.Fatalf("a large file: %+v %+v", big, rerr)
	}
	// A project with no startup files has nothing to trust.
	bare := config.CanonicalPath(t.TempDir())
	c.ok("workspace.save", map[string]any{"path": bare, "name": "bare", "mounts": []any{}})
	if r := laptop.call("trust.set", map[string]any{"repo": bare, "revision": "abcd"}); r.Error == nil || !strings.Contains(r.Error.Message, "nothing") && !strings.Contains(r.Error.Message, "no init.scm") {
		t.Fatalf("no startup files: %+v", r.Error)
	}
	if _, err := os.Stat(filepath.Join(o.Paths.ConfigDir, "trusted-projects.json")); err != nil {
		t.Fatal(err)
	}
}
