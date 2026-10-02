package guard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/scheme"
)

type fixed struct {
	name string
	d    Decision
}

func (f fixed) Name() string                                                 { return f.name }
func (f fixed) Check(context.Context, *scheme.Prim, []scheme.Value) Decision { return f.d }

var prim = &scheme.Prim{Name: "p", Meta: scheme.Meta{Doc: "d"}}

type decisionLog struct {
	decisions []log.GuardDecision
}

func (l *decisionLog) Record(_ log.Kind, payload any) (log.Event, error) {
	l.decisions = append(l.decisions, payload.(log.GuardDecision))
	return log.Event{}, nil
}

func TestDecisionExplanationPreservesCheckerOrder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		checkers   []Checker
		decision   string
		checker    string
		reason     string
		detail     string
		wantPrompt bool
	}{
		{
			name: "confirmation reasons retain order",
			checkers: []Checker{
				fixed{"first", Decision{Verdict: Confirm, Reason: "first reason", Detail: "first detail"}},
				fixed{"second", Decision{Verdict: Confirm, Reason: "second reason", Detail: "second detail"}},
			},
			decision: "confirm", checker: "first", reason: "first reason; second reason",
			detail: "first detail\n\nsecond detail", wantPrompt: true,
		},
		{
			name: "first denial stops evaluation",
			checkers: []Checker{
				fixed{"first", Decision{Verdict: Confirm, Reason: "confirm reason"}},
				fixed{"denial", Decision{Verdict: Deny, Reason: "deny reason"}},
				fixed{"later", Decision{Verdict: Deny, Reason: "must not run"}},
			},
			decision: "deny", checker: "denial", reason: "confirm reason; deny reason",
		},
		{
			name: "security confirmation survives standing approval",
			checkers: []Checker{
				fixed{"standing", Decision{Verdict: Allow, Standing: true, Reason: "approved"}},
				fixed{"security", Decision{Verdict: Confirm, Security: true, Reason: "secret", Detail: "review"}},
			},
			decision: "confirm", checker: "security", reason: "secret", detail: "review", wantPrompt: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var recorded decisionLog
			var prompts []Prompt
			g := New(ConfirmFunc(func(_ context.Context, p Prompt) (bool, error) {
				prompts = append(prompts, p)
				return true, nil
			}), &recorded, tc.checkers...)
			err := g.Before(context.Background(), prim, nil)
			if (err != nil) != (tc.decision == "deny") {
				t.Fatalf("decision error: %v", err)
			}
			if len(recorded.decisions) != 1 {
				t.Fatalf("decisions: %+v", recorded.decisions)
			}
			got := recorded.decisions[0]
			if got.Decision != tc.decision || got.Checker != tc.checker || got.Reason != tc.reason {
				t.Fatalf("unexpected explanation: %+v", got)
			}
			if (len(prompts) == 1) != tc.wantPrompt {
				t.Fatalf("prompts: %+v", prompts)
			}
			if tc.wantPrompt && (prompts[0].Detail != tc.detail || strings.Join(prompts[0].Reasons, "; ") != tc.reason || got.Answer != "yes") {
				t.Fatalf("prompt and record disagree: %+v, %+v", prompts[0], got)
			}
		})
	}
}

func TestStrictestVerdictWins(t *testing.T) {
	asked := 0
	yes := ConfirmFunc(func(context.Context, Prompt) (bool, error) { asked++; return true, nil })

	g := New(yes, nil, fixed{"a", Decision{Verdict: Allow}}, fixed{"b", Decision{Verdict: Confirm, Reason: "b says confirm"}}, fixed{"c", Decision{Verdict: Allow}})
	if err := g.Before(context.Background(), prim, nil); err != nil || asked != 1 {
		t.Fatalf("confirm path: err=%v asked=%d", err, asked)
	}

	g = New(yes, nil, fixed{"b", Decision{Verdict: Confirm, Reason: "x"}}, fixed{"d", Decision{Verdict: Deny, Reason: "d says deny"}}, fixed{"e", Decision{Verdict: Confirm, Reason: "never reached"}})
	err := g.Before(context.Background(), prim, nil)
	var d *scheme.Denied
	if !errors.As(err, &d) || d.Reason != "d says deny" || asked != 1 {
		t.Fatalf("deny must win without prompting: %v asked=%d", err, asked)
	}
}

func TestNilConfirmerDenies(t *testing.T) {
	g := New(nil, nil, fixed{"b", Decision{Verdict: Confirm, Reason: "needs a yes"}})
	err := g.Before(context.Background(), prim, nil)
	if err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("%v", err)
	}
}

func TestConfirmerCancellationPropagates(t *testing.T) {
	blocking := ConfirmFunc(func(ctx context.Context, _ Prompt) (bool, error) { <-ctx.Done(); return false, ctx.Err() })
	g := New(blocking, nil, fixed{"b", Decision{Verdict: Confirm, Reason: "r"}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Before(ctx, prim, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("want Canceled, got %v", err)
	}
}

func TestArgsSummaryTruncates(t *testing.T) {
	long := scheme.Str(strings.Repeat("x", 500))
	s := summarizeArgs([]scheme.Value{long, scheme.Int(1)})
	if len(s) > 140 || !strings.Contains(s, "...") {
		t.Fatalf("summary not truncated: %d", len(s))
	}
}

func TestStandingApprovalOutranksConfirmNotDeny(t *testing.T) {
	shell := &scheme.Prim{Name: "shell-exec", Meta: scheme.Meta{Doc: "d", Irreversible: true, Params: []scheme.Param{
		{Name: "cmd", Type: "string", Role: "argv"}, {Name: "args", Type: "list", Role: "argv"},
	}}}
	argv := func(prog string, rest ...string) []scheme.Value {
		items := make([]scheme.Value, len(rest))
		for i, r := range rest {
			items[i] = scheme.Str(r)
		}
		return []scheme.Value{scheme.Str(prog), scheme.List(items...)}
	}
	allow := NewCommandAllow("go test", "git status")
	asked := 0
	g := New(ConfirmFunc(func(context.Context, Prompt) (bool, error) { asked++; return false, nil }), nil, MetaPolicy{}, DefaultArgvTrigger("/repo"), allow)

	if err := g.Before(context.Background(), shell, argv("go", "test", "./...")); err != nil || asked != 0 {
		t.Fatalf("go test should pass on standing approval: %v asked=%d", err, asked)
	}
	if err := g.Before(context.Background(), shell, argv("/usr/local/go/bin/go", "test")); err != nil {
		t.Fatalf("basename match: %v", err)
	}
	if err := g.Before(context.Background(), shell, argv("go", "build")); err == nil || asked != 1 {
		t.Fatalf("go build is not approved: %v asked=%d", err, asked)
	}
	allow.Add("go build")
	if err := g.Before(context.Background(), shell, argv("go", "build")); err != nil {
		t.Fatalf("after Add: %v", err)
	}
	// A deny still wins: the Egress checker denies an unbound secret-bearing
	// call, and a standing approval must not override it.
	egress := &scheme.Prim{Name: "http", Meta: scheme.Meta{Doc: "d", Egress: true, Params: []scheme.Param{
		{Name: "cred", Type: "any", Role: "credential"}, {Name: "url", Type: "string", Role: "dest"},
	}}}
	eg := Egress{Table: binding.Table{}} // empty table: nothing is bound
	g = New(ConfirmFunc(func(context.Context, Prompt) (bool, error) { return false, nil }), nil, MetaPolicy{}, DefaultArgvTrigger("/repo"), allow, eg)
	allow.Add("http")
	if err := g.Before(context.Background(), egress, []scheme.Value{scheme.Value{Kind: scheme.KHandle, H: &scheme.Handle{Class: "openai-key", ID: "ab"}}, scheme.Str("https://api.openai.com/v1/chat")}); err == nil {
		t.Fatal("standing approval must not override an egress deny")
	}
	if got := strings.Join(allow.List(), ","); got != "git status,go build,go test,http" {
		t.Fatalf("list: %s", got)
	}
	for argvLine, want := range map[string]string{"go test ./x": "go test", "rg -n foo": "rg", "git status": "git status", "./bin/nodwyn version": "nodwyn version", "sh -c ls": "sh"} {
		if got := Prefix(strings.Fields(argvLine)); got != want {
			t.Errorf("Prefix(%q) = %q, want %q", argvLine, got, want)
		}
	}
}

type failingDecisionLog struct{ err error }

func (l failingDecisionLog) Record(log.Kind, any) (log.Event, error) { return log.Event{}, l.err }

func TestAuditFailureStopsHostOperation(t *testing.T) {
	failed := errors.New("audit unavailable")
	p := &scheme.Prim{Name: "host-op", Meta: scheme.Meta{Host: true}}
	for _, verdict := range []Verdict{Allow, Confirm, Deny} {
		t.Run(verdict.String(), func(t *testing.T) {
			g := New(ConfirmFunc(func(context.Context, Prompt) (bool, error) { return true, nil }), failingDecisionLog{failed}, fixed{"policy", Decision{Verdict: verdict}})
			if err := g.Before(context.Background(), p, nil); !errors.Is(err, failed) {
				t.Fatalf("audit failure not propagated: %v", err)
			}
		})
	}
}

// Yolo answers the guard's questions; it never overturns its refusals.
func TestAutoAllowAnswersConfirmsButNotDenials(t *testing.T) {
	ordinary := &scheme.Prim{Name: "fs-write", Meta: scheme.Meta{Doc: "d", Irreversible: true}}
	g := New(AutoAllow, nil, MetaPolicy{})
	if err := g.Before(context.Background(), ordinary, nil); err != nil {
		t.Fatalf("ordinary confirm should pass: %v", err)
	}
	// A security confirm is exactly the case a human was the control for.
	secure := fixed{"scanner", Decision{Verdict: Confirm, Security: true, Reason: "looks like a credential"}}
	g = New(AutoAllow, nil, MetaPolicy{}, secure)
	if err := g.Before(context.Background(), ordinary, nil); err == nil {
		t.Fatal("security confirm must not be auto-approved")
	}
	// And a deny is not a question at all.
	g = New(AutoAllow, nil, fixed{"pin", Decision{Verdict: Deny, Reason: "outside the repository"}})
	err := g.Before(context.Background(), ordinary, nil)
	var denied *scheme.Denied
	if !errors.As(err, &denied) || denied.Reason != "outside the repository" {
		t.Fatalf("deny must stand under AutoAllow: %v", err)
	}
}

func TestRepoPinMounts(t *testing.T) {
	root, ref, shared, outside := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	pin := RepoPin{Root: root, Mounts: []Mount{{Path: ref}, {Path: shared, Write: true}}}
	write := &scheme.Prim{Name: "fs-write", Meta: scheme.Meta{Irreversible: true, Params: []scheme.Param{{Name: "path", Role: "path"}, {Name: "content", Role: "payload"}}}}
	read := &scheme.Prim{Name: "fs-read", Meta: scheme.Meta{Params: []scheme.Param{{Name: "path", Role: "path"}}}}
	check := func(p *scheme.Prim, path string) Verdict {
		return pin.Check(context.Background(), p, []scheme.Value{scheme.Str(path), scheme.Str("x")}).Verdict
	}
	for _, tt := range []struct {
		p    *scheme.Prim
		path string
		want Verdict
	}{
		{read, "notes.md", Allow},
		{write, "notes.md", Allow},
		{read, filepath.Join(ref, "contract.pdf"), Allow},
		{write, filepath.Join(ref, "contract.pdf"), Deny},
		{write, filepath.Join(shared, "handbook.md"), Allow},
		{read, filepath.Join(outside, "x"), Deny},
		{read, "../" + filepath.Base(ref) + "/x", Deny},
	} {
		if got := check(tt.p, tt.path); got != tt.want {
			t.Errorf("%s %s: %v, want %v", tt.p.Name, tt.path, got, tt.want)
		}
	}
	if pin.Resolve(filepath.Join(ref, "a")) == "" || pin.Resolve(filepath.Join(outside, "a")) != "" {
		t.Fatal("Resolve disagrees with Check")
	}
	// A link inside a read-only mount pointing into a writable one is judged by its target.
	os.Symlink(shared, filepath.Join(ref, "link"))
	if got := check(write, filepath.Join(ref, "link", "f")); got != Allow {
		t.Fatalf("link into a writable mount: %v", got)
	}
	os.Symlink(outside, filepath.Join(shared, "escape"))
	if got := check(write, filepath.Join(shared, "escape", "f")); got != Deny {
		t.Fatalf("link out of a mount: %v", got)
	}
}

func TestDenylistResolvesAgainstTheRoot(t *testing.T) {
	config := t.TempDir()
	d := PathDenylist{Dirs: []string{config}, Root: config}
	write := &scheme.Prim{Name: "fs-write", Meta: scheme.Meta{Irreversible: true, Params: []scheme.Param{{Name: "path", Role: "path"}, {Name: "content", Role: "payload"}}}}
	if d.Check(context.Background(), write, []scheme.Value{scheme.Str("bindings.toml"), scheme.Str("x")}).Verdict != Deny {
		t.Fatal("a relative path into a denied directory was allowed")
	}
	other := t.TempDir()
	os.Symlink(config, filepath.Join(other, "cfg"))
	d.Root = other
	if d.Check(context.Background(), write, []scheme.Value{scheme.Str("cfg/bindings.toml"), scheme.Str("x")}).Verdict != Deny {
		t.Fatal("a link into a denied directory was allowed")
	}
}

func TestMountsKeepProtectedPathsAndInnermostAccess(t *testing.T) {
	root, outer := t.TempDir(), t.TempDir()
	inner := filepath.Join(outer, "ref")
	os.MkdirAll(inner, 0o755)
	write := &scheme.Prim{Name: "fs-write", Meta: scheme.Meta{Irreversible: true, Params: []scheme.Param{{Name: "path", Role: "path"}, {Name: "content", Role: "payload"}}}}
	pp := ProtectedPaths{Root: root, Mounts: []Mount{{Path: outer, Write: true}}}
	d := pp.Check(context.Background(), write, []scheme.Value{scheme.Str(filepath.Join(outer, ".git", "hooks", "pre-commit")), scheme.Str("x")})
	if d.Verdict != Confirm || !d.Security {
		t.Fatalf("a mounted repository's hook: %+v", d)
	}
	pin := RepoPin{Root: root, Mounts: []Mount{{Path: outer, Write: true}, {Path: inner}}}
	if pin.Check(context.Background(), write, []scheme.Value{scheme.Str(filepath.Join(inner, "x")), scheme.Str("x")}).Verdict != Deny {
		t.Fatal("a read-only mount inside a writable one was writable")
	}
}

// A source is read, never written: it is pinned and checked against the
// denylist like a path, a read-only mount may supply it to an irreversible
// primitive, and a primitive with a path and a source writes its path.
func TestSourceRole(t *testing.T) {
	root, ref, outside := t.TempDir(), t.TempDir(), t.TempDir()
	export := &scheme.Prim{Name: "doc-export", Meta: scheme.Meta{Irreversible: true, Params: []scheme.Param{{Name: "source", Role: "source"}, {Name: "target", Role: "path"}}}}
	upload := &scheme.Prim{Name: "upload", Meta: scheme.Meta{Irreversible: true, Egress: true, Params: []scheme.Param{{Name: "path", Role: "source"}, {Name: "title", Role: "payload"}}}}
	pin := RepoPin{Root: root, Mounts: []Mount{{Path: ref}}}
	args := func(a, b string) []scheme.Value { return []scheme.Value{scheme.Str(a), scheme.Str(b)} }
	for _, tt := range []struct {
		p          *scheme.Prim
		src, other string
		want       Verdict
	}{
		{export, "notes.md", "notes.docx", Allow},
		{export, filepath.Join(ref, "brief.md"), "brief.docx", Allow},
		{export, "notes.md", filepath.Join(ref, "notes.docx"), Deny},
		{export, filepath.Join(outside, "x.md"), "x.docx", Deny},
		{upload, filepath.Join(ref, "brief.docx"), "Brief", Allow},
		{upload, filepath.Join(outside, "x.docx"), "X", Deny},
	} {
		if got := pin.Check(context.Background(), tt.p, args(tt.src, tt.other)).Verdict; got != tt.want {
			t.Errorf("%s %s %s: %v, want %v", tt.p.Name, tt.src, tt.other, got, tt.want)
		}
	}
	if d := DefaultDenylist().Check(context.Background(), export, args(".env.md", "x.docx")); d.Verdict != Deny {
		t.Fatalf("a credential-shaped source: %+v", d)
	}
	if path, ok := WritePath(export, args("notes.md", "notes.docx")); !ok || path != "notes.docx" {
		t.Fatalf("doc-export writes %q %v", path, ok)
	}
	if path, ok := WritePath(upload, args("notes.docx", "Notes")); ok {
		t.Fatalf("an upload is not a write, got %q", path)
	}
}

// Code named like credential material is code: a task about credentials
// reads and changes credentials.go and credentials.js. The data files,
// dotfiles and extension patterns stay refused.
func TestDenylistLeavesCodeAlone(t *testing.T) {
	d := DefaultDenylist()
	for _, path := range []string{"internal/frontend/rpc/credentials.go", "web/assets/credentials.js", "id_map.py", "docs/credentials.md"} {
		if why := d.Denies(path); why != "" {
			t.Errorf("%s: %s", path, why)
		}
	}
	for _, path := range []string{"credentials", "credentials.json", "config/credentials.yml.enc", ".env", ".env.js", ".env.md", "id_rsa", "id_ed25519.pub", "server.key", "cert.pem", ".netrc"} {
		if d.Denies(path) == "" {
			t.Errorf("%s is not refused", path)
		}
	}
}

// The session's scratch directory lives in nodwyn's data directory, which
// the denylist protects; the scratch directory itself is usable, the rest
// of the data directory is not, and credential-shaped names are refused in
// the scratch directory too.
func TestDenylistExceptsTheScratchDirectory(t *testing.T) {
	data := t.TempDir()
	scratch := filepath.Join(data, "scratch", "nodwyn-scratch-1")
	os.MkdirAll(scratch, 0o700)
	d := PathDenylist{Globs: []string{".env"}, Dirs: []string{data}, Except: []string{scratch}}
	if why := d.Denies(filepath.Join(scratch, "probe.py")); why != "" {
		t.Fatalf("the scratch directory is denied: %s", why)
	}
	if d.Denies(filepath.Join(data, "sessions", "x.jsonl")) == "" {
		t.Fatal("the rest of the data directory is usable")
	}
	if d.Denies(filepath.Join(scratch, ".env")) == "" {
		t.Fatal("a credential-shaped name in the scratch directory is usable")
	}
	// A link in the scratch directory that leads elsewhere in the data
	// directory is judged by where it leads.
	os.MkdirAll(filepath.Join(data, "sessions"), 0o700)
	os.Symlink(filepath.Join(data, "sessions"), filepath.Join(scratch, "out"))
	if d.Denies(filepath.Join(scratch, "out", "x.jsonl")) == "" {
		t.Fatal("a link out of the scratch directory escaped the denylist")
	}
}
