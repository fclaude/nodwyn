// Package prim holds the host primitives: the Go handlers through which
// Scheme — and therefore the model — touches the filesystem and processes.
// Every one is registered with metadata the guard reads (ADR 4); none of
// them talk to the network in bootstrap (plan §3.3).
package prim

import (
	"os"
	"path/filepath"
	"time"

	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/scheme"
)

// Options configure the host primitives.
type Options struct {
	Root         string        // repository root; every path is pinned under it
	MaxReadBytes int64         // fs-read refuses larger files
	MaxOutput    int           // shell-exec truncates stdout/stderr beyond this
	ShellTimeout time.Duration // per shell-exec cap
	EnvAllow     []string      // environment variables children may see
}

// DefaultOptions returns the bootstrap defaults for a repository root.
func DefaultOptions(root string) Options {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return Options{
		Root:         filepath.Clean(abs),
		MaxReadBytes: 1 << 20,
		MaxOutput:    256 << 10,
		ShellTimeout: 10 * time.Minute,
		EnvAllow:     []string{"PATH", "HOME", "GOPATH", "GOCACHE", "GOFLAGS", "GOMODCACHE", "GOTOOLCHAIN", "TERM", "LANG", "LC_ALL", "TMPDIR", "USER", "SHELL"},
	}
}

// Register adds the host primitives to in.
func Register(in *scheme.Interp, o Options) error {
	if o.Root == "" {
		return os.ErrInvalid
	}
	pin := guard.RepoPin{Root: o.Root}
	for _, p := range []*scheme.Prim{fsRead(o, pin), fsWrite(o, pin), fsEdit(o, pin), fsList(o, pin), shellExec(o)} {
		if err := in.Register(p); err != nil {
			return err
		}
	}
	return nil
}

// Checkers returns the guard checkers the host primitives rely on, in the
// order they should run: the envelope from plan §3.3, the write preview,
// and the user's standing command approvals (nil for none).
func Checkers(o Options, allow *guard.CommandAllow) []guard.Checker {
	cs := []guard.Checker{
		guard.MetaPolicy{},
		guard.RepoPin{Root: o.Root},
		guard.DefaultDenylist(),
		guard.DefaultArgvTrigger(o.Root),
		WritePreview{},
	}
	if allow != nil {
		cs = append(cs, allow)
	}
	return cs
}
