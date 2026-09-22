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

	// P3 egress (all optional; nil disables the egress primitives — bootstrap
	// has none). The guard's E3 checker enforces the binding table before any
	// of these handlers run; the handlers only do the work the guard allowed.
	// The core implements CredentialStore: it owns the keychain, the masked
	// input, and the log.
	Credentials CredentialStore
	// P5 Jev (ADR 19/20): the noul-gate service. nil disables the jev-noul /
	// jev-score / jev-choice primitives (the gate is "plan" — file-only mode,
	// ADR 20 degradation). The core implements JevClient: it owns the URL,
	// timeout, content level, redactor, transport, and the jev.req log.
	Jev JevClient
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

// Register adds the host primitives to in. When o.Keychain is set (P3) the
// egress primitives — http-request, credential-register — are registered
// too; they are guarded by the E3 checker before they run.
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
	if o.Credentials != nil {
		for _, p := range []*scheme.Prim{httpRequest(o.Credentials), credentialRegister(o.Credentials)} {
			if err := in.Register(p); err != nil {
				return err
			}
		}
	}
	if o.Jev != nil {
		for _, p := range []*scheme.Prim{jevNoul(o.Jev), jevScore(o.Jev), jevChoice(o.Jev)} {
			if err := in.Register(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// Checkers returns the guard checkers the host primitives rely on, in the
// order they should run: the envelope from plan §3.3, the write preview,
// and the user's standing command approvals (nil for none). extra carries
// the phase-specific checkers — the E3 egress checker from P3 — appended
// after the core ones.
func Checkers(o Options, allow *guard.CommandAllow, writes *guard.WriteAllow, extra ...guard.Checker) []guard.Checker {
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
	if writes != nil {
		cs = append(cs, writes)
	}
	cs = append(cs, extra...)
	return cs
}
