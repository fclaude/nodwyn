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

// IsProtected reports whether rel is one of the always-prompt paths.
func IsProtected(rel string) bool {
	rel = filepath.ToSlash(filepath.Clean(rel))
	for _, p := range Protected {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(rel, p) {
				return true
			}
		} else if rel == p {
			return true
		}
	}
	return false
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
