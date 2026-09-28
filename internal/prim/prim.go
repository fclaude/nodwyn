// Package prim holds the host primitives: the Go handlers through which
// Scheme — and therefore the model — touches the filesystem and processes.
// Every one is registered with metadata the guard reads (ADR 4). Egress
// handlers use shared host services for credentials and transport.
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
	Root         string // repository root; every path is pinned under it
	Scratch      string // the session's scratch directory, outside Root; "" for none
	MaxReadBytes int64  // fs-read refuses larger files, unless it reads a line range
	// MaxRangeBytes caps what a ranged fs-read returns: whole lines up to
	// this many bytes, so the window never cuts a ranged read (the core
	// sets it from window.max_result_tokens).
	MaxRangeBytes int
	MaxOutput     int           // shell-exec truncates stdout/stderr beyond this
	ShellTimeout  time.Duration // per shell-exec cap
	EnvAllow      []string      // environment variables children may see

	// P3 egress (all optional; nil disables the egress primitives — bootstrap
	// has none). The guard's E3 checker enforces the binding table before any
	// of these handlers run; the handlers only do the work the guard allowed.
	// The core implements CredentialStore: it owns the keychain, the masked
	// input, and the log.
	Credentials CredentialStore
	// Configured policy paths; only named policy operations can access them.
	BindingsPath string
	// Jev is optional: nil disables its primitives. The host owns credential
	// injection, redaction, transport and recording; settings are ordinary data.
	Jev       JevClient
	JevConfig JevOptions
}

// DefaultOptions returns the bootstrap defaults for a repository root.
func DefaultOptions(root string) Options {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return Options{
		Root:          filepath.Clean(abs),
		MaxReadBytes:  1 << 20,
		MaxRangeBytes: 3*3072 - 256,
		MaxOutput:     256 << 10,
		ShellTimeout:  10 * time.Minute,
		EnvAllow:      []string{"PATH", "HOME", "GOPATH", "GOCACHE", "GOFLAGS", "GOMODCACHE", "GOTOOLCHAIN", "TERM", "LANG", "LC_ALL", "TMPDIR", "USER", "SHELL"},
	}
}

// Register adds the host primitives to in. When o.Credentials is set (P3) the
// egress primitives — http-request, credential-register — are registered
// too; they are guarded by the E3 checker before they run.
func Register(in *scheme.Interp, o Options) error {
	if o.Root == "" {
		return os.ErrInvalid
	}
	pin := guard.RepoPin{Root: o.Root, Scratch: o.Scratch}
	for _, p := range []*scheme.Prim{fsRead(o, pin), fsWrite(o, pin), fsEdit(o, pin), fsList(o, pin), shellExec(o), indexGrep(o, pin), fsFind(o, pin)} {
		if err := in.Register(p); err != nil {
			return err
		}
	}
	for _, p := range []*scheme.Prim{policyRead(o), policyWrite(o), credential(o.Credentials), scratchDir(o), repository(o)} {
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
		for _, p := range []*scheme.Prim{jevNoul(o.JevConfig, o.Jev), jevScore(o.JevConfig, o.Jev), jevChoice(o.JevConfig, o.Jev)} {
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
	argv := guard.DefaultArgvTrigger(o.Root)
	argv.Scratch = o.Scratch
	cs := []guard.Checker{
		guard.MetaPolicy{},
		guard.RepoPin{Root: o.Root, Scratch: o.Scratch},
		guard.DefaultDenylist(),
		argv,
		guard.ProtectedPaths{Root: o.Root},
		guard.ScratchWrites{Dir: o.Scratch},
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

// scratchDir builds (scratch-dir): the session's scratch directory, for a
// prompt or a strategy to name. Files there are outside the repository, so
// they never reach its diff, and the directory is removed when the session
// closes.
func scratchDir(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "scratch-dir",
		Meta: scheme.Meta{
			Doc:    "The session's scratch directory: outside the repository, writable without prompts, removed when the session closes; #f when there is none.",
			Hidden: true,
		},
		Handler: func(*scheme.Call) (scheme.Value, error) {
			if o.Scratch == "" {
				return scheme.False, nil
			}
			return scheme.Str(o.Scratch), nil
		},
	}
}

// repository builds (repository): the repository's root, for a prompt or a
// strategy to name.
func repository(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "repository",
		Meta: scheme.Meta{Doc: "The repository's root directory: every relative path is relative to it.", Hidden: true},
		Handler: func(*scheme.Call) (scheme.Value, error) {
			return scheme.Str(o.Root), nil
		},
	}
}
