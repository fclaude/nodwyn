package guard

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/fclaude/nibb/internal/scheme"
)

// WriteAllow holds standing approvals for writes by path prefix, the
// counterpart of CommandAllow for fs-write/fs-edit (ADR 17, amended). A
// prefix is a repository-relative directory ("internal/", "docs/") or file.
// Entries come from config, the repository's .nibb/allow.toml, and "a"
// answers at a write prompt (session only).
//
// Some paths are never covered by a standing approval, whatever the list
// says: the ones that change policy or run in CI. Those always prompt.
type WriteAllow struct {
	mu       sync.RWMutex
	prefixes []string
}

// Protected paths can never be standing-approved.
var Protected = []string{".nibb/", ".github/", ".gitea/", "scripts/", "Makefile", ".git/"}

// NewWriteAllow builds the checker.
func NewWriteAllow(entries ...string) *WriteAllow {
	w := &WriteAllow{}
	for _, e := range entries {
		w.Add(e)
	}
	return w
}

// Add registers a prefix ("" and protected paths are ignored).
func (w *WriteAllow) Add(prefix string) {
	p := strings.TrimSpace(prefix)
	if p == "" || p == "." || p == "/" {
		return
	}
	p = filepath.ToSlash(filepath.Clean(p))
	if p == "." {
		return
	}
	if strings.HasSuffix(prefix, "/") {
		p += "/"
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, e := range w.prefixes {
		if e == p {
			return
		}
	}
	w.prefixes = append(w.prefixes, p)
}

// List returns the entries, sorted.
func (w *WriteAllow) List() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := append([]string(nil), w.prefixes...)
	sort.Strings(out)
	return out
}

// Match returns the matching prefix for a repository-relative path, or "".
// Protected paths never match.
func (w *WriteAllow) Match(rel string) string {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if IsProtected(rel) {
		return ""
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, p := range w.prefixes {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(rel, p) {
				return p
			}
		} else if rel == p {
			return p
		}
	}
	return ""
}

// IsProtected reports whether rel is one of the always-prompt paths. Case
// is ignored: on a case-insensitive file system (macOS's default) .GIT/hooks
// is .git/hooks.
func IsProtected(rel string) bool {
	rel = strings.ToLower(filepath.ToSlash(filepath.Clean(rel)))
	for _, p := range Protected {
		p = strings.ToLower(p)
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(rel, p) || rel+"/" == p {
				return true
			}
		} else if rel == p {
			return true
		}
	}
	return false
}

// ProtectedPaths makes every write to a protected path a security confirm.
// Those files hold code that later runs with the user's authority — git
// hooks, CI definitions, the Makefile and scripts, and .nibb/init.scm, which
// nibb loads as the user — so each write needs a person: no standing or
// turn approval waives it, and unattended runs (AutoAllow) refuse it.
type ProtectedPaths struct {
	Root string // the repository; paths are judged relative to it
	// Mounts are judged relative to their own folder: a mounted repository's
	// .git/hooks is as protected as the root's.
	Mounts []Mount
}

// Name identifies the checker in the log.
func (ProtectedPaths) Name() string { return "protected-path" }

// Check judges the path as the repository pin does: absolute paths and
// symbolic links are resolved first, so neither reaches a protected file
// under another name.
func (pp ProtectedPaths) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	raw, ok := WritePath(p, args)
	if !ok {
		return Decision{Verdict: Allow}
	}
	rel := raw
	if r, ok := (RepoPin{Root: pp.Root, Mounts: pp.Mounts}).MountRel(raw); ok {
		rel = r
	} else if abs := (RepoPin{Root: pp.Root}).Resolve(raw); abs != "" {
		if r, err := filepath.Rel(resolveExisting(pp.Root), resolveExisting(abs)); err == nil {
			rel = r
		}
	}
	if !IsProtected(rel) {
		return Decision{Verdict: Allow}
	}
	return Decision{Verdict: Confirm, Security: true,
		Reason: "protected path " + filepath.ToSlash(rel) + ": it holds code that later runs with your authority"}
}

func (*WriteAllow) Name() string { return "write-allow" }

// Check turns a matching write into a standing Allow. Only irreversible
// primitives with a path and a payload parameter (writes) are considered.
func (w *WriteAllow) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	rel, ok := WritePath(p, args)
	if !ok {
		return Decision{Verdict: Allow}
	}
	if m := w.Match(rel); m != "" {
		return Decision{Verdict: Allow, Standing: true, Reason: "standing write approval: " + m}
	}
	return Decision{Verdict: Allow}
}

// WritePath returns the repository-relative path of a write primitive
// application (irreversible, with path and payload roles), or ok=false.
func WritePath(p *scheme.Prim, args []scheme.Value) (string, bool) {
	if !p.Meta.Irreversible {
		return "", false
	}
	path, payload := -1, false
	for i, pa := range p.Meta.Params {
		switch pa.Role {
		case "path":
			path = i
		case "payload":
			payload = true
		}
	}
	if path < 0 || !payload || path >= len(args) || args[path].Kind != scheme.KString {
		return "", false
	}
	return args[path].S, true
}

// DirPrefix suggests the standing-approval entry for a write: the file's
// directory with a trailing slash, or the file itself at the root.
func DirPrefix(rel string) string {
	rel = filepath.ToSlash(filepath.Clean(rel))
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		return rel
	}
	return dir + "/"
}

// ScratchWrites approves, as standing, every write inside the session's
// scratch directory: it is outside the repository and removed when the
// session closes, so asking about each throwaway file protects nothing. A
// security confirm, such as content that looks like a credential, still
// asks.
type ScratchWrites struct {
	Dir string // the scratch directory; "" approves nothing
}

// Name identifies the checker in the log.
func (ScratchWrites) Name() string { return "scratch" }

// Check approves an absolute write path that resolves inside Dir.
func (sw ScratchWrites) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	raw, ok := WritePath(p, args)
	if !ok || sw.Dir == "" || !filepath.IsAbs(raw) || !(RepoPin{Root: sw.Dir}).inside(raw) {
		return Decision{Verdict: Allow}
	}
	return Decision{Verdict: Allow, Standing: true, Reason: "scratch directory " + sw.Dir}
}
