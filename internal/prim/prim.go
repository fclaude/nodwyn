// Package prim holds the host primitives: the Go handlers through which
// Scheme — and therefore the model — touches the filesystem and processes.
// Every one is registered with metadata the guard reads (ADR 4). Egress
// handlers use shared host services for credentials and transport.
package prim

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// Options configure the host primitives.
type Options struct {
	Root      string // repository root; every path is pinned under it
	Scratch   string // the session's scratch directory, outside Root; "" for none
	NotesFile string // this repository\'s notes kept across sessions, outside Root; "" for none
	// Workspace (ADR 22): its kind ("docs" registers no shell-exec), the
	// folders outside Root it may use, and the version store that keeps a
	// file's previous content before a write where git is not the undo.
	Kind         string
	Mounts       []guard.Mount
	Versions     *Versions
	MaxReadBytes int64 // fs-read refuses larger files, unless it reads a line range
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
	pin := guard.RepoPin{Root: o.Root, Scratch: o.Scratch, Mounts: o.Mounts}
	prims := []*scheme.Prim{fsRead(o, pin), fsWrite(o, pin), fsEdit(o, pin), fsList(o, pin), indexGrep(o, pin), fsFind(o, pin)}
	if o.Kind != "docs" {
		prims = append(prims, shellExec(o))
	}
	for _, p := range prims {
		if err := in.Register(p); err != nil {
			return err
		}
	}
	for _, p := range []*scheme.Prim{policyRead(o), policyWrite(o), credential(o.Credentials), scratchDir(o), repository(o), workspace(o), projectNotes(o), projectNotesSave(o)} {
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
		guard.WorkspaceKind{Kind: o.Kind},
		guard.RepoPin{Root: o.Root, Scratch: o.Scratch, Mounts: o.Mounts},
		denylist(o.Root),
		argv,
		guard.ProtectedPaths{Root: o.Root, Mounts: o.Mounts},
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

func denylist(root string) guard.PathDenylist {
	d := guard.DefaultDenylist()
	d.Root = root
	return d
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

// workspace builds (workspace): the workspace's kind and its mounted
// folders, as an alist: (("kind" . "docs") ("mounts" (("path" . P) ("access" . A)) …)).
func workspace(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "workspace",
		Meta: scheme.Meta{Doc: "This workspace's kind (code, docs or mixed) and the folders outside it it may use.", Hidden: true},
		Handler: func(*scheme.Call) (scheme.Value, error) {
			kind := o.Kind
			if kind == "" {
				kind = "code"
			}
			var mounts []scheme.Value
			for _, m := range o.Mounts {
				access := "read"
				if m.Write {
					access = "write"
				}
				mounts = append(mounts, scheme.List(scheme.Cons(scheme.Str("path"), scheme.Str(m.Path)), scheme.Cons(scheme.Str("access"), scheme.Str(access))))
			}
			return scheme.List(scheme.Cons(scheme.Str("kind"), scheme.Str(kind)), scheme.Cons(scheme.Str("mounts"), scheme.List(mounts...))), nil
		},
	}
}

// maxProjectNotes caps a repository's notes: they ride in every session's
// system prompt.
const maxProjectNotes = 8 << 10

// projectNotes builds (project-notes): this repository's notes kept across
// sessions, "" when there are none. They live beside nodwyn's data, not in
// the repository, so they never reach its diff.
func projectNotes(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "project-notes",
		Meta: scheme.Meta{Doc: "This repository's notes kept across sessions (how tests run, where things are, runbooks), \"\" when there are none.", Hidden: true},
		Handler: func(*scheme.Call) (scheme.Value, error) {
			if o.NotesFile == "" {
				return scheme.Str(""), nil
			}
			b, err := os.ReadFile(o.NotesFile)
			if errors.Is(err, os.ErrNotExist) {
				return scheme.Str(""), nil
			}
			if err != nil {
				return scheme.Value{}, err
			}
			return scheme.Str(string(b)), nil
		},
	}
}

// projectNotesSave builds (project-notes-save text): replace this
// repository's notes. Secrets are redacted before they are written, and the
// notes are capped, since later sessions read them as part of their prompt.
func projectNotesSave(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "project-notes-save",
		Meta: scheme.Meta{
			Doc:     "Replace this repository's notes kept across sessions (at most 8 KiB; secrets are redacted).",
			Params:  []scheme.Param{{Name: "text", Type: "string", Doc: "the notes", Role: "payload"}},
			Hidden:  true,
			Audited: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			if o.NotesFile == "" {
				return scheme.Value{}, errors.New("this session keeps no project notes (no data directory)")
			}
			text, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			text = redact.Redact(text, nil).Text
			if len(text) > maxProjectNotes {
				return scheme.Value{}, fmt.Errorf("project notes are %d bytes; at most %d: keep what later sessions need", len(text), maxProjectNotes)
			}
			if err := os.MkdirAll(filepath.Dir(o.NotesFile), 0o700); err != nil {
				return scheme.Value{}, err
			}
			tmp, err := os.CreateTemp(filepath.Dir(o.NotesFile), ".notes-*")
			if err != nil {
				return scheme.Value{}, err
			}
			_, werr := tmp.WriteString(text)
			cerr := tmp.Close()
			if werr != nil || cerr != nil {
				os.Remove(tmp.Name())
				return scheme.Value{}, errors.Join(werr, cerr)
			}
			if err := os.Rename(tmp.Name(), o.NotesFile); err != nil {
				os.Remove(tmp.Name())
				return scheme.Value{}, err
			}
			return scheme.Void, nil
		},
	}
}
