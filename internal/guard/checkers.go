package guard

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/fclaude/nibb/internal/scheme"
)

// pathArgs yields the (index, value) of every parameter with Role "path".
func pathArgs(p *scheme.Prim, args []scheme.Value) []string {
	var out []string
	for i, pa := range p.Meta.Params {
		if pa.Role == "path" && i < len(args) && args[i].Kind == scheme.KString {
			out = append(out, args[i].S)
		}
	}
	return out
}

// CommandArgs yields the command line of a primitive with Role "argv" parameters
// (a command string and a list of arguments).
func CommandArgs(p *scheme.Prim, args []scheme.Value) []string {
	var out []string
	for i, pa := range p.Meta.Params {
		if pa.Role != "argv" || i >= len(args) {
			continue
		}
		switch args[i].Kind {
		case scheme.KString:
			out = append(out, args[i].S)
		case scheme.KPair, scheme.KNil:
			items, _ := args[i].Slice()
			for _, it := range items {
				if it.Kind == scheme.KString {
					out = append(out, it.S)
				}
			}
		}
	}
	return out
}

// MetaPolicy applies the primitive's own declaration: irreversible actions
// need confirmation. Egress is decided by the E3 checker (guard.Egress),
// which owns the binding table — not here.
type MetaPolicy struct{}

func (MetaPolicy) Name() string { return "meta" }

func (MetaPolicy) Check(_ context.Context, p *scheme.Prim, _ []scheme.Value) Decision {
	if p.Meta.Irreversible {
		if p.Meta.ConfirmReason != "" {
			return Decision{Verdict: Confirm, Reason: p.Meta.ConfirmReason}
		}
		return Decision{Verdict: Confirm, Reason: p.Name + " is irreversible"}
	}
	return Decision{Verdict: Allow}
}

// RepoPin denies path arguments that resolve outside the repository root
// (plan §3.3), or outside the session's scratch directory when there is
// one. Symlinks are resolved so a link out of either does not escape the
// pin. Relative paths are always relative to the repository.
type RepoPin struct {
	Root    string // absolute, cleaned
	Scratch string // the session's scratch directory; "" for none
	// Mounts are the workspace's folders outside the root (ADR 22 §1),
	// reached by absolute path. A read-only mount refuses every write.
	Mounts []Mount
}

// Mount is a folder outside the root a workspace may use.
type Mount struct {
	Path  string // absolute, cleaned
	Write bool
}

func (RepoPin) Name() string { return "repo-pin" }

func (r RepoPin) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	for _, raw := range pathArgs(p, args) {
		if !r.inside(raw) {
			where := "the repository " + r.Root
			if r.Scratch != "" {
				where += " and the scratch directory " + r.Scratch
			}
			if len(r.Mounts) > 0 {
				where += " and the workspace's mounted folders"
			}
			return Decision{Verdict: Deny, Reason: "path " + raw + " is outside " + where}
		}
		if m := r.mountOf(raw); m != nil && !m.Write && p.Meta.Irreversible {
			return Decision{Verdict: Deny, Reason: "path " + raw + " is in " + m.Path + ", which this workspace mounts read-only"}
		}
	}
	return Decision{Verdict: Allow}
}

// mountOf returns the mount raw resolves into, or nil for the root, the
// scratch directory and anything else.
func (r RepoPin) mountOf(raw string) *Mount {
	if !filepath.IsAbs(raw) {
		return nil
	}
	resolved := resolveExisting(filepath.Clean(raw))
	if within(resolved, resolveExisting(r.Root)) {
		return nil
	}
	for i := range r.Mounts {
		if within(resolved, resolveExisting(r.Mounts[i].Path)) {
			return &r.Mounts[i]
		}
	}
	return nil
}

// Resolve returns the absolute path for raw under the root, or "" if it
// escapes. Exported for the primitives, which must use the same rule.
func (r RepoPin) Resolve(raw string) string {
	abs := raw
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(r.Root, abs)
	}
	abs = filepath.Clean(abs)
	// Resolve symlinks on the longest existing prefix so a new file under a
	// linked directory is judged by where the link points.
	resolved := resolveExisting(abs)
	if within(resolved, resolveExisting(r.Root)) {
		return abs
	}
	if r.Scratch != "" && filepath.IsAbs(raw) && within(resolved, resolveExisting(r.Scratch)) {
		return abs
	}
	if filepath.IsAbs(raw) && r.mountOf(raw) != nil {
		return abs
	}
	return ""
}

// WorkspaceKind refuses what a workspace's kind leaves out (ADR 22): a
// docs workspace runs no programs and defines no tools, whoever asks.
type WorkspaceKind struct {
	Kind string
}

func (WorkspaceKind) Name() string { return "workspace-kind" }

func (w WorkspaceKind) Check(_ context.Context, p *scheme.Prim, _ []scheme.Value) Decision {
	if w.Kind == "docs" && (p.Name == "shell-exec" || p.Name == "register-tool") {
		return Decision{Verdict: Deny, Reason: "a docs workspace runs no programs and defines no tools (" + p.Name + ")"}
	}
	return Decision{Verdict: Allow}
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

func (r RepoPin) inside(raw string) bool { return r.Resolve(raw) != "" }

func resolveExisting(p string) string {
	cur := p
	var rest []string
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				real = filepath.Join(real, rest[i])
			}
			return real
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// PathDenylist denies path arguments that look like credential material,
// whatever their location (plan §3.3). It is defense in depth and stays
// after L1 lands.
type PathDenylist struct {
	Globs []string // matched against the base name
	Dirs  []string // absolute directories that are entirely off limits
}

// DefaultDenylist is the bootstrap list.
func DefaultDenylist() PathDenylist {
	home, _ := os.UserHomeDir()
	d := PathDenylist{Globs: []string{".env", ".env.*", "*.pem", "*.key", "id_*", "*.kdbx", "*.p12", "*.pfx", "*.jks", "credentials", "credentials.*", ".netrc", ".npmrc", ".pypirc"}}
	if home != "" {
		for _, sub := range []string{".ssh", ".aws", ".gnupg", ".config/nibb", ".config/gcloud", ".azure", ".kube"} {
			d.Dirs = append(d.Dirs, filepath.Join(home, sub))
		}
	}
	return d
}

func (PathDenylist) Name() string { return "path-denylist" }

func (d PathDenylist) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	for _, raw := range pathArgs(p, args) {
		base := filepath.Base(raw)
		for _, g := range d.Globs {
			if ok, _ := filepath.Match(g, base); ok {
				return Decision{Verdict: Deny, Reason: "path " + raw + " matches the credential-file denylist (" + g + ")"}
			}
		}
		abs, _ := filepath.Abs(raw)
		for _, dir := range d.Dirs {
			if abs == dir || strings.HasPrefix(abs, dir+string(filepath.Separator)) {
				return Decision{Verdict: Deny, Reason: "path " + raw + " is under the protected directory " + dir}
			}
		}
	}
	return Decision{Verdict: Allow}
}

// ArgvTrigger asks for confirmation when a command line names a network
// tool, a destructive git/rm invocation, or an absolute path outside the
// repository. It is the bootstrap stand-in for L1-on-argv and the P5 hard
// trigger list (plan §3.3).
type ArgvTrigger struct {
	Root     string
	Scratch  string   // the session's scratch directory, which is not "outside"
	Network  []string // program names
	Patterns []string // substrings of the joined command line
}

// DefaultArgvTrigger is the bootstrap list.
func DefaultArgvTrigger(root string) ArgvTrigger {
	return ArgvTrigger{
		Root:     root,
		Network:  []string{"curl", "wget", "nc", "ncat", "netcat", "ssh", "scp", "sftp", "rsync", "telnet", "ftp", "socat"},
		Patterns: []string{"git push", "git reset --hard", "git clean -f", "rm -rf", "rm -fr", "sudo ", "chmod 777"},
	}
}

func (ArgvTrigger) Name() string { return "argv-trigger" }

func (a ArgvTrigger) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	argv := CommandArgs(p, args)
	if len(argv) == 0 {
		return Decision{Verdict: Allow}
	}
	line := strings.Join(argv, " ")
	for _, tok := range argv {
		base := filepath.Base(tok)
		for _, n := range a.Network {
			if base == n {
				return Decision{Verdict: Confirm, Reason: "command line uses the network tool " + n, Detail: line}
			}
		}
		if filepath.IsAbs(tok) {
			if pin := (RepoPin{Root: a.Root, Scratch: a.Scratch}); !pin.inside(tok) && !isSystemBinary(tok) {
				return Decision{Verdict: Confirm, Reason: "command line names a path outside the repository: " + tok, Detail: line}
			}
		}
	}
	for _, pat := range a.Patterns {
		if strings.Contains(line, pat) {
			return Decision{Verdict: Confirm, Reason: "command line matches the hard-trigger pattern " + strings.TrimSpace(pat), Detail: line}
		}
	}
	return Decision{Verdict: Allow}
}

// isSystemBinary allows absolute paths to executables in the usual bin
// directories without a prompt.
func isSystemBinary(p string) bool {
	for _, prefix := range []string{"/usr/bin/", "/bin/", "/usr/local/bin/", "/opt/homebrew/bin/", "/usr/sbin/", "/sbin/"} {
		if strings.HasPrefix(p, prefix) && !strings.Contains(strings.TrimPrefix(p, prefix), "/") {
			return true
		}
	}
	return false
}
