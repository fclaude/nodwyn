// Package prim holds the host primitives: the Go handlers through which
// Scheme — and therefore the model — touches the filesystem and processes.
// Every one is registered with metadata the guard reads (ADR 4). Egress
// handlers use shared host services for credentials and transport.
package prim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/guard"
	nlog "github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// Options configure the host primitives.
type Options struct {
	Root      string // repository root; every path is pinned under it
	Scratch   string // the session's scratch directory, outside Root; "" for none
	NotesFile string // this repository\'s notes kept across sessions, outside Root; "" for none
	// Workspace (ADR 22): the folders outside Root it may use, and the
	// version store that keeps a file's previous content before a write where
	// git is not the undo.
	Mounts   []guard.Mount
	Versions *Versions
	// Worktree is the git worktree Root is, when the session was given one
	// of its own (ADR 34); nil otherwise.
	Worktree     *Worktree
	MaxReadBytes int64 // fs-read refuses larger files, unless it reads a line range
	// MaxRangeBytes caps what a ranged fs-read returns: whole lines up to
	// this many bytes, so the window never cuts a ranged read (the core
	// sets it from window.max_result_tokens).
	MaxRangeBytes int
	// RangeBytes, when set, is that cap as it is now: the window's cap on
	// a result grows with the model's context, which is known only once
	// the session has sized its window, and changes with the model.
	RangeBytes   func() int
	MaxOutput    int           // shell-exec truncates stdout/stderr beyond this
	ShellTimeout time.Duration // per shell-exec cap
	EnvAllow     []string      // environment variables children may see
	// Procs holds the session's background processes (shell-start and the
	// rest); nil offers none, as in nodwyn repl.
	Procs *Procs
	// Terms holds the session's terminals (term-open and the rest,
	// ADR 48); nil offers none, which is the default: they are experimental
	// and offered only when [experimental] terminal is set.
	Terms *Terms
	// Keeper keeps the changes as they stood at a passing test run, for a
	// turn that a hard limit stops (keep.go); nil keeps nothing.
	Keeper *Keeper
	// IndexDir is where the project's repository index lives, in nodwyn's
	// data directory and shared by the project's sessions (ADR 50); ""
	// keeps none, and offers no index-update. IndexRecord records the
	// summary of each build or refresh (index.update).
	IndexDir    string
	IndexRecord func(nlog.IndexUpdate)
	// Conversation is index-search's scopes the core keeps (ADR 50): scope
	// "conversation", the session's own, and "earlier", the project's other
	// tasks; nil for none.
	Conversation func(ctx context.Context, scope, query string, limit int) (string, error)

	// Lean is the lean profile's tool surface (ADR 32, amended 2026-10-17):
	// the outline, tests-for and scheme tools, fs-read's symbol, index-grep's
	// search of source files alone in a tree too large to search whole, and
	// the count of lines an edit added and removed. The full profile's tools
	// are the ones they were before.
	Lean bool
	// Temp is the system's /tmp (guard.TempDir), which a lean session nobody
	// approves may use as it uses Scratch, and where a path outside both is
	// refused outright (ADR 32, amended 2026-10-17); "" for none. The core
	// sets it.
	Temp string
	// Sandbox is sandbox mode (ADR 55), which only a run started with
	// --sandbox container is in: the machine is the session's disposable
	// sandbox. The pin then allows any path, the argv trigger refuses only
	// paths in the denylist's directories, of the protected paths only
	// nodwyn's folder needs a person, and what a shell-exec command leaves
	// running in the background keeps running after it returns.
	Sandbox bool

	// P3 egress (all optional; nil disables the egress primitives — bootstrap
	// has none). The guard's E3 checker enforces the binding table before any
	// of these handlers run; the handlers only do the work the guard allowed.
	// The core implements CredentialStore: it owns the keychain, the masked
	// input, and the log.
	Credentials CredentialStore
	// Configured policy paths; only named policy operations can access them.
	BindingsPath string
	// Private are nodwyn's own configuration and data directories as the
	// session resolved them (XDG overrides included); the denylist refuses
	// them to every path argument, like the default places it names.
	Private []string
	// Judge is optional: nil disables its primitives. The host owns
	// credential injection, redaction, transport and recording; settings are
	// ordinary data. JudgeConfig is the judge the task starts with, which
	// sets the primitives' egress metadata and whether the model is offered
	// them (Tools); each call asks Judge for the judge the task has then
	// (ADR 47).
	Judge       JudgeClient
	JudgeConfig JudgeOptions

	// Documents (W5). Pandoc is the program doc-export runs; "" finds
	// pandoc on PATH. Docs is optional: nil offers no google-doc-upload,
	// which also needs DocsConfig.GoogleClass. The core is the DocsClient.
	Pandoc     string
	Docs       DocsClient
	DocsConfig DocsOptions
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
	pin := guard.RepoPin{Root: o.Root, Scratch: o.Scratch, Mounts: o.Mounts, Temp: o.Temp, Sandbox: o.Sandbox}
	prims := []*scheme.Prim{fsRead(o, pin), fsWrite(o, pin), fsEdit(o, pin), fsList(o, pin), indexGrep(o, pin), fsFind(o, pin), shellExec(o), docExport(o, pin)}
	if o.Lean {
		// The lean profile's additions to the tools (ADR 32, amended
		// 2026-10-17), offered to a control model only when lean-tools names
		// them: a full session's catalogue is the one it always had.
		prims = append(prims, outlinePrim(o, pin), testsForPrim(o, pin), schemeTool())
	}
	prims = append(prims, indexPrims(o, pin)...)
	if isGitWorkTree(o.Root) {
		prims = append(prims, gitPrims(o, pin)...)
		prims = append(prims, checkWithout(o), planEvidence(o), testBefore(o), headHasNamePrim(o), docsMentioningPrim(o), gitIntend(o), changeRemovalsPrim(o), changeClaimsPrim(o), droppedOperandsPrim(o), originalTestsPrim(o))
		if o.Worktree != nil {
			prims = append(prims, gitLand(o))
		}
	}
	if o.Procs != nil {
		prims = append(prims, procPrims(o, o.Procs)...)
	}
	if o.Terms != nil {
		prims = append(prims, termPrims(o, o.Terms)...)
	}
	for _, p := range prims {
		if err := in.Register(p); err != nil {
			return err
		}
	}
	for _, p := range []*scheme.Prim{policyRead(o), policyWrite(o), credential(o.Credentials), scratchDir(o), shellCommandsPrim(), readData(), requestNamesPrim(), editSupportPrim(), repository(o), workspace(o), worktree(o), projectNotes(o), projectNotesSave(o), projectInstructions(o), keepPassing(o)} {
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
	if o.Judge != nil {
		for _, p := range []*scheme.Prim{judgeYesNo(o.JudgeConfig, o.Judge), judgeScore(o.JudgeConfig, o.Judge), judgeChoice(o.JudgeConfig, o.Judge), judgeAvailable(o.Judge)} {
			if err := in.Register(p); err != nil {
				return err
			}
		}
	}
	if o.Docs != nil && o.DocsConfig.GoogleClass != "" {
		if err := in.Register(googleDocUpload(o.DocsConfig, o.Docs, pin)); err != nil {
			return err
		}
	}
	return nil
}

// Checkers returns the guard checkers the host primitives rely on, in the
// order they should run: the envelope (metadata policy, repository pin, path denylist, argv trigger), the write preview,
// and the user's standing command approvals (nil for none). extra carries
// the phase-specific checkers — the E3 egress checker from P3 — appended
// after the core ones.
func Checkers(o Options, allow *guard.CommandAllow, writes *guard.WriteAllow, extra ...guard.Checker) []guard.Checker {
	argv := guard.DefaultArgvTrigger(o.Root)
	argv.Scratch, argv.Temp = o.Scratch, o.Temp
	deny := denylist(o)
	if o.Sandbox {
		argv.Sandbox = &deny
	}
	cs := []guard.Checker{
		guard.MetaPolicy{},
		guard.RepoPin{Root: o.Root, Scratch: o.Scratch, Mounts: o.Mounts, Temp: o.Temp, Sandbox: o.Sandbox},
		deny,
		argv,
		guard.ProtectedPaths{Root: o.Root, Mounts: o.Mounts, Sandbox: o.Sandbox},
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

// denylist is the credential denylist for a session in o.Root. It adds
// nodwyn's own directories where the session found them to the default
// places. The scratch directory is in the system's temporary directory,
// outside the directories the list protects; should TMPDIR put that inside
// one of them, it is the one place there the session's tools may use.
func denylist(o Options) guard.PathDenylist {
	d := guard.DefaultDenylist()
	d.Root = o.Root
	for _, dir := range o.Private {
		if filepath.IsAbs(dir) && !slices.Contains(d.Dirs, filepath.Clean(dir)) {
			d.Dirs = append(d.Dirs, filepath.Clean(dir))
		}
	}
	if o.Scratch != "" {
		d.Except = []string{o.Scratch}
	}
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

// shellCommandsPrim builds (shell-commands cmd args): the simple commands a
// command line runs (guard.ShellCommands), for the evidence loop to match a
// report's citations against.
func shellCommandsPrim() *scheme.Prim {
	return &scheme.Prim{
		Name: "shell-commands",
		Meta: scheme.Meta{
			Doc: "The simple commands a command line runs, each as its words from the program on joined by spaces: a shell's -c line read as the shell reads it (a here-document's body, a quoted pattern and a redirection's target are no command; a substitution's commands are left out), anything else as one command. () for a line that does not parse.",
			Params: []scheme.Param{
				{Name: "cmd", Type: "string", Doc: "the program"},
				{Name: "args", Type: "list", Doc: "its arguments"},
			},
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			name, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			items, err := c.List(1)
			if err != nil {
				return scheme.Value{}, err
			}
			argv := []string{name}
			for _, it := range items {
				if it.Kind != scheme.KString {
					return scheme.Value{}, errors.New("args must be a list of strings")
				}
				argv = append(argv, it.S)
			}
			cmds := guard.ShellCommands(argv)
			out := make([]scheme.Value, len(cmds))
			for i, s := range cmds {
				out[i] = scheme.Str(s)
			}
			return scheme.List(out...), nil
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

// workspace builds (workspace): the workspace's mounted folders, as an
// alist: (("mounts" (("path" . P) ("access" . A)) …)).
func workspace(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "workspace",
		Meta: scheme.Meta{Doc: "The folders outside this workspace it may use.", Hidden: true},
		Handler: func(*scheme.Call) (scheme.Value, error) {
			var mounts []scheme.Value
			for _, m := range o.Mounts {
				access := "read"
				if m.Write {
					access = "write"
				}
				mounts = append(mounts, scheme.List(scheme.Cons(scheme.Str("path"), scheme.Str(m.Path)), scheme.Cons(scheme.Str("access"), scheme.Str(access))))
			}
			return scheme.List(scheme.Cons(scheme.Str("mounts"), scheme.List(mounts...))), nil
		},
	}
}

// Worktree is a session's own git worktree (ADR 34): Root is a linked
// worktree of Repository on Branch, made from Base at BaseCommit.
type Worktree struct{ Repository, Branch, Base, BaseCommit string }

// worktree builds (worktree): #f, or the session's worktree as an alist, for
// a prompt or a strategy to say where the work goes.
func worktree(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "worktree",
		Meta: scheme.Meta{Doc: "The git worktree made for this session: its repository, branch, base branch and base commit; #f when the session works in the folder itself.", Hidden: true},
		Handler: func(*scheme.Call) (scheme.Value, error) {
			w := o.Worktree
			if w == nil {
				return scheme.False, nil
			}
			return scheme.List(
				scheme.Cons(scheme.Str("repository"), scheme.Str(w.Repository)),
				scheme.Cons(scheme.Str("branch"), scheme.Str(w.Branch)),
				scheme.Cons(scheme.Str("base"), scheme.Str(w.Base)),
				scheme.Cons(scheme.Str("base-commit"), scheme.Str(w.BaseCommit)),
			), nil
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

// instructionFiles are the names a repository's instructions for coding
// agents go by, in the order they are looked for: AGENTS.md, as the
// convention spells it, then in lower case.
var instructionFiles = []string{"AGENTS.md", "agents.md"}

// maxProjectInstructions caps what of AGENTS.md rides in every session's
// system prompt; the rest stays in the file for fs-read.
const maxProjectInstructions = 32 << 10

// projectInstructions builds (project-instructions): the repository's
// AGENTS.md, its instructions for coding agents, "" when it has none. The
// file is the repository's, so it may be anything a clone brought: it is
// read only as a regular file inside the root after links are followed and
// past the credential denylist under both names, a link to a key elsewhere
// reads as no file, and secrets in it are redacted, since the text goes to
// the model with every request.
func projectInstructions(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "project-instructions",
		Meta: scheme.Meta{Doc: "The repository's AGENTS.md, its instructions for coding agents (at most 32 KiB; secrets are redacted), \"\" when it has none.", Hidden: true},
		Handler: func(*scheme.Call) (scheme.Value, error) {
			if o.Root == "" {
				return scheme.Str(""), nil
			}
			pin, deny := guard.RepoPin{Root: o.Root}, denylist(o)
			for _, name := range instructionFiles {
				path := filepath.Join(o.Root, name)
				real, err := filepath.EvalSymlinks(path)
				if err != nil {
					continue
				}
				st, err := os.Stat(real)
				if err != nil || !st.Mode().IsRegular() || pin.Resolve(path) == "" || deny.Denies(path) != "" || deny.Denies(real) != "" {
					continue
				}
				b, err := os.ReadFile(real)
				if err != nil {
					return scheme.Value{}, err
				}
				text := strings.ToValidUTF8(string(b), "")
				if len(text) > maxProjectInstructions {
					cut := strings.LastIndexByte(text[:maxProjectInstructions], '\n')
					if cut < 0 {
						cut = maxProjectInstructions
					}
					text = strings.ToValidUTF8(text[:cut], "") + "\n[" + name + " is longer: read the rest with fs-read]"
				}
				return scheme.Str(redact.Redact(text, nil).Text), nil
			}
			return scheme.Str(""), nil
		},
	}
}

// projectNotesSave builds (project-notes-save text): replace this
// repository's notes. Secrets are redacted before they are written, and the
// notes are capped, since later sessions read them as part of their prompt.
// It is a tool the model calls: the system prompt calls the notes the
// agent's own, and until 2026-10-05 only a person could write them from the
// console, so no model ever did (the local session logs of 2026-09-30 to
// 10-05 hold no call, and no repository had notes; session
// 20261005T170658Z-fe9ea317) (ADR 32, amended 2026-10-05).
func projectNotesSave(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "project-notes-save",
		Meta: scheme.Meta{
			Doc:     "Replace this repository's notes, which every later session here is shown with its instructions: how its tests run, setup steps, traps you fell into, where things are that are hard to find. Write the notes you were shown, kept where still true, plus a line or two of what you learned; not what the code or its documentation already says, and never a secret: a save that looks like one needs the person's approval. At most 8 KiB. They live beside nodwyn's data, never in the repository.",
			Params:  []scheme.Param{{Name: "text", Type: "string", Doc: "the whole notes, as later sessions should read them", Role: "payload"}},
			Host:    true,
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
