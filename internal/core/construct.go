package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/container"
	"github.com/fclaude/nodwyn/internal/detect/l1"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/loop"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/scheme"
	"github.com/fclaude/nodwyn/internal/window"
)

// Options build a Session.
type Options struct {
	Config   config.Config
	Paths    config.Paths
	Repo     string // repository root; every path is pinned under it
	Version  string
	Provider llm.Provider // the control model's provider; nil: built from Config.Control
	// NoConfirm answers every confirm with "no" (non-interactive runs).
	NoConfirm bool
	// AutoApprove answers ordinary confirms with "yes" and security
	// confirms with "no" (guard.AutoAllow). Denials are unaffected: this
	// answers the guard's questions, it does not overrule its refusals.
	AutoApprove bool
	// JudgeApprove has the judge answer ordinary confirms, decided in Go
	// (judge.go) so no Scheme can approve a call; security confirms are
	// declined without asking, and a failure is a no. It needs a judge: a
	// [judge] server whose key is stored (ADR 18 §5, ADR 20 Seam C, ADR 46).
	JudgeApprove bool
	// Unattended means nobody is there to answer anything (an unattended
	// run, ADR 18 §5): no prompt ever waits for a person. Tool confirms are
	// declined, or answered as AutoApprove answers them when it is set too;
	// a soft budget checkpoint continues up to the hard limit, and both are
	// recorded as answered by "unattended".
	Unattended bool
	// InitFile explicitly authorises an init file on behalf of a Go embedder.
	// Empty loads <Repo>/.nodwyn/init.scm only after user-owned project trust.
	InitFile string
	// Model is the model this conversation talks to (ADR 40). The zero
	// choice is config.toml's [control] model. A session resumed from a log
	// that switched its model takes that choice back, when config.toml
	// still has it.
	Model ModelChoice
	// Judge is the judge the task asks (ADR 47); nil is config.toml's
	// [judge] server. A session resumed from a log that chose its judge
	// takes that choice back, when config.toml still has it.
	Judge *JudgeChoice
	// Delegates are the configured models, besides the one the task talks
	// to, its model may hand work to with the delegate tool (ADR 27, amended
	// 2026-10-09). None, the default, offers no such tool. A session resumed
	// from a log that chose some takes them back, those config.toml still has.
	Delegates *DelegateChoice
	// Limits says whether the task's turns stop at the turn limits (ADR
	// 49); nil is on. A session resumed from a log that chose takes that
	// choice back.
	Limits *LimitsChoice
	// EventBuffer is the events channel capacity (default 256).
	EventBuffer int
	// LogWriter overrides the session log destination (tests).
	LogWriter io.Writer
	// P3 egress (optional; nil disables egress primitives — bootstrap).
	// Keychain is an opened keychain store (the passphrase was already
	// provided by the front-end). Bindings is an in-memory table used only
	// when Paths.Bindings is empty; a configured file is authoritative. An empty
	// table denies every secret-bearing egress (ADR 6).
	Keychain *keychain.Store
	Bindings binding.Table
	// Containers is the runtime contained MCP servers run with (ADR 23
	// W3b); nil: the one config.toml names, else podman, else docker, as
	// found on PATH. Tests give a fake.
	Containers *container.Runtime
	// Worktree starts the session in a new git worktree of Repo, on a
	// branch of its own, instead of in Repo (ADR 34). The person asks for
	// it; nothing the model does can.
	Worktree bool
	// Sandbox is sandbox mode (ADR 54): the person declares the whole
	// machine the session's disposable sandbox, as a benchmark's task
	// container is (nodwyn run --sandbox container). The guard then lets
	// the tools reach any path but nodwyn's own directories and credential
	// files, what a command starts in the background keeps running, and
	// nobody is asked anything: it implies AutoApprove and Unattended, and
	// excludes JudgeApprove. Nothing turns it on but the run's flag.
	Sandbox bool
}

// unattended is whether nobody approves this session's calls: it answers
// ordinary confirms itself (--yolo, a run's auto_approve, judged approval),
// or nobody is there at all. (unattended?) tells the same to Scheme, and the
// default loop's profile follows it unless config.toml or the environment
// choose (config.Loop.ResolveProfile).
func (o Options) unattended() bool {
	return o.AutoApprove || o.JudgeApprove || o.Unattended
}

// New assembles a session, loads initialization under the guard, freezes the
// loop's tool list, and records session.start.
func New(o Options) (*Session, error) {
	var err error
	o, err = resolveOptions(o)
	if err != nil {
		return nil, err
	}
	s := &Session{
		opts:      o,
		id:        log.NewSessionID(time.Now()),
		out:       make(chan event.Event, o.EventBuffer),
		prompts:   map[string]pendingConfirmation{},
		secrets:   map[string]pendingSecret{},
		judge:     initialJudge(o),
		delegates: initialDelegates(o),
	}
	if o.Limits != nil && !o.Limits.On {
		s.limitsOff, s.limitsActor = true, o.Limits.Actor
	}
	// The worktree comes first: it is the root everything else loads from
	// (init.scm, allow.toml). A session that never starts
	// takes it away again, with its branch.
	var made log.WorktreeChange
	if o.Worktree {
		if made, err = newWorktree(o.Repo, s.id); err != nil {
			return nil, err
		}
		s.worktree = &made.Worktree
		o.Repo = made.Path
		s.opts.Repo = made.Path
	}
	started := false
	defer func() {
		if !started && s.worktree != nil {
			discardWorktree(*s.worktree)
		}
	}()
	created := ""
	if o.LogWriter != nil {
		s.logw = log.New(o.LogWriter, s.id)
	} else {
		created = log.SessionPath(o.Paths.SessionsDir, s.id)
		s.logw, err = log.Create(created, s.id)
		if err != nil {
			return nil, err
		}
	}

	if err := s.initialize(); err != nil {
		s.logw.Close()
		// A session that never started leaves no log behind: nothing is in
		// it yet, and an empty file would show as a broken session.
		if created != "" {
			os.Remove(created)
		}
		return nil, err
	}
	s.flushOutput()
	recipe := s.loop.WindowContext()
	_, err = s.Record(log.KindSessionStart, log.SessionStart{
		Context: recipe,
		Version: o.Version, Repo: o.Repo, Provider: s.opts.Provider.Name(),
		Model: s.model.Model, URL: s.model.URL, Tools: len(s.loop.Tools()),
		Worktree: s.worktree,
	})
	if err == nil && o.Judge != nil {
		// A task opened with a judge of its own records it, so resuming it
		// asks that judge again whatever [judge] says later (ADR 47).
		_, err = s.Record(log.KindSessionJudge, judgeRecord(o.Config, o.Judge.Server, o.Judge.Actor))
	}
	if err == nil && o.Delegates != nil && len(o.Delegates.Models) > 0 {
		// The models the task may hand work to are recorded, so resuming it
		// offers the same ones whatever the picker defaults to later.
		_, err = s.Record(log.KindSessionDelegates, log.SessionDelegates{Actor: o.Delegates.Actor, Models: o.Delegates.Models})
	}
	if err == nil && s.limitsOff {
		// A task opened with its limits off records it, so resuming it, as
		// a schedule does for every run, keeps them off (ADR 49).
		_, err = s.Record(log.KindSessionLimits, log.SessionLimits{Actor: s.limitsActor, On: false})
	}
	if c := s.opts.Model; err == nil && (c.Name != loop.ControlModel || s.effort != "") {
		// A conversation opened on a chosen model, or asking for an effort,
		// records the choice as a switch does, so resuming it talks to that
		// model again, asking that effort (ADR 40, ADR 41): what it was
		// opened on is not what config.toml's defaults say later.
		_, err = s.Record(log.KindSessionModel, modelRecord("", c, s.model, s.effort, recipe))
	}
	if err != nil {
		s.closeMCP()
		s.logw.Close()
		removeScratch(s.scratch)
		return nil, err
	}
	started = true
	if s.worktree != nil {
		_, _ = s.Record(log.KindWorktreeCreate, made)
	}
	s.markStarted()
	return s, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func resolveOptions(o Options) (Options, error) {
	if _, err := o.Config.Budget.Limits(o.Config.Loop.MaxToolCalls); err != nil {
		return Options{}, err
	}
	// The choice is resolved once, here, so a refusal says what is wrong
	// before a session exists; initialize resolves it into the model the
	// conversation talks to. config.toml's [control] model stays where it
	// is, so a conversation can be moved back to it (ADR 40).
	if _, _, err := resolveChoice(o.Config, o.Model); err != nil {
		return Options{}, err
	}
	if o.Model.Name == "" && o.Model.Server == "" && o.Model.Model == "" {
		o.Model.Name = loop.ControlModel
	}
	// A judge chosen for the task is checked as a choice made later is
	// (ADR 47), so a refusal says what is wrong before a session exists.
	if o.Judge != nil {
		if err := CheckJudge(o.Config, o.Keychain, o.Judge.Server); err != nil {
			return Options{}, fmt.Errorf("judge: %w", err)
		}
	}
	if o.Delegates != nil {
		models, err := checkDelegates(o.Config, o.Delegates.Models)
		if err != nil {
			return Options{}, err
		}
		o.Delegates = &DelegateChoice{Models: models, Actor: o.Delegates.Actor}
	}
	if o.Repo == "" {
		return Options{}, errors.New("core: repo root is required")
	}
	if o.Sandbox {
		// Nobody watches a sandbox: its ordinary confirms are answered yes
		// and its security ones declined, as --yolo --unattended has them.
		if o.JudgeApprove || o.NoConfirm {
			return Options{}, errors.New("core: sandbox mode answers ordinary confirms itself (as --yolo), so it excludes the judge and --no-confirm")
		}
		o.AutoApprove, o.Unattended = true, true
	}
	repo, err := filepath.Abs(o.Repo)
	if err != nil {
		return Options{}, err
	}
	if err := migrateNotes(o.Paths, repo); err != nil {
		return Options{}, err
	}
	o.Repo = config.CanonicalPath(repo)
	if _, err := config.MigrateFolder(o.Repo); err != nil {
		return Options{}, fmt.Errorf("core: moving .%s to %s: %w", config.LegacyName, config.FolderDir, err)
	}
	if o.EventBuffer == 0 {
		o.EventBuffer = 256
	}
	return o, nil
}

// rangeBytes is what a recall returns at most: under the window's cut for
// one tool result (window.Estimate is bytes/3), with room for its header, as
// the window is sized now once the loop has it (ADR 9, amended 2026-10-05).
// A ranged fs-read has twice it where the window shows an fs-read up to
// twice the cut (primitiveOptions, ADR 9, amended 2026-10-16).
func (s *Session) rangeBytes() int {
	if s.loop == nil {
		return s.opts.Config.Window.MaxResultTokens*3 - 256
	}
	return s.loop.ResultCap()*3 - 256
}

// privateDirs are the directories no path argument of a task may name:
// nodwyn's own configuration and data, and the directory a service manager
// put the daemon's credentials in, where the keychain's passphrase may be
// (ADR 5, amended 2026-10-17), which any process of the same account can
// otherwise read.
func privateDirs(p config.Paths) []string {
	dirs := []string{p.ConfigDir, p.DataDir}
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); filepath.IsAbs(dir) {
		dirs = append(dirs, dir)
	}
	return dirs
}

func (s *Session) primitiveOptions() prim.Options {
	o := s.opts
	_, shellTimeout := o.Config.Loop.Durations()
	po := prim.DefaultOptions(o.Repo)
	po.Lean = s.profile == config.ProfileLean
	if po.Lean && o.unattended() {
		// A lean session nobody approves may use /tmp as its scratch
		// directory (ADR 32, amended 2026-10-17).
		po.Temp = guard.TempDir()
	}
	po.Scratch = s.scratch
	po.Sandbox = o.Sandbox
	po.Private = privateDirs(o.Paths)
	po.Procs = s.procs
	po.Terms = s.terms
	// Notes are the project's, so a worktree's session shares them with
	// the repository's (ADR 34).
	po.NotesFile = notesFile(o.Paths.DataDir, s.project())
	if w := s.worktree; w != nil {
		po.Worktree = &prim.Worktree{Repository: w.Repository, Branch: w.Branch, Base: w.Base, BaseCommit: w.BaseCommit}
	}
	for _, m := range s.workspace.Mounts {
		po.Mounts = append(po.Mounts, guard.Mount{Path: m.Path, Write: m.Access == "write"})
	}
	po.Versions = &prim.Versions{Dir: s.versionsDir(), Skip: prim.GitHoldsPath(o.Repo), Record: func(v prim.Version) {
		_, _ = s.Record(log.KindFileVersion, log.FileVersion{Path: v.Path, Hash: v.Hash, Bytes: v.Bytes})
	}}
	// A ranged fs-read stays under the window's cut for one fs-read result
	// (window.Estimate is bytes/3, Policy.ReadCap twice the cut on other
	// results), with room for its header: the cut as the window is sized
	// now, once the loop has it (ADR 9, amended 2026-10-05 and 2026-10-16).
	// A recall is cut as other results are (rangeBytes).
	po.MaxRangeBytes = 2*o.Config.Window.MaxResultTokens*3 - 256
	po.RangeBytes = func() int {
		if s.loop == nil {
			return po.MaxRangeBytes
		}
		return s.loop.ReadCap()*3 - 256
	}
	// Retrieval (ADR 50): the conversation is the session's; the
	// repository's index is the project's, so a worktree's session shares
	// the repository's, beside its notes.
	po.Conversation = s.searchConversation
	if dir := indexDir(o.Paths.DataDir, s.project()); dir != "" {
		po.IndexDir = dir
		po.IndexRecord = func(u log.IndexUpdate) { _, _ = s.Record(log.KindIndexUpdate, u) }
	}
	po.BindingsPath = o.Paths.Bindings
	po.ShellTimeout = shellTimeout
	// P3 egress: the session is the CredentialStore (it owns the keychain,
	// the masked prompt, and the log). Bootstrap leaves this nil.
	s.keychain = o.Keychain
	if s.keychain != nil {
		po.Credentials = s
	}
	// The judge (ADR 19/20, ADR 46, ADR 47): its egress primitives are
	// registered when a judge could be asked at all (a keychain is open,
	// the judge is not off, and config.toml has a judge provider), since
	// the primitive table is fixed for the session while the task's judge
	// may be chosen later. The session is the JudgeClient, and each call
	// asks it for the judge the task has then. They are the model's tools
	// only at the "full" content level, since the model chooses what it
	// sends, and only when the task starts with a judge it can ask: the
	// tool list cannot gain them later.
	if s.keychain != nil && judgeContent(o.Config) != config.JudgeContentNone && len(o.Config.JudgeChoices()) > 0 {
		po.Judge = s
		po.JudgeConfig, _ = s.JudgeOptions()
		po.JudgeConfig.Tools = judgeContent(o.Config) == config.JudgeContentFull && s.judgeKey() != nil
	}
	// W5 documents: google-doc-upload is offered only when a credential of
	// the configured class is stored now, since the tool list is fixed for
	// the session. The session is the DocsClient.
	// Uploads set to deny are not offered at all (uploads.go).
	if class := o.Config.Docs.GoogleCredential; class != "" && s.HandleForClass(class) != nil && s.uploadPolicy() != config.UploadDeny {
		po.Docs = s
		po.DocsConfig = prim.DocsOptions{GoogleClass: class, GoogleUploadURL: o.Config.Docs.GoogleUploadURL}
	}
	// What an unattended turn's passing test runs keep, for a hard stop to
	// put back (keep.go).
	po.Keeper = prim.NewKeeper(po)
	s.keeper = po.Keeper
	return po
}

// newGuard builds policy in its existing order. Confirmation remains disabled
// until initialization finishes, so startup never waits for an absent frontend.
func (s *Session) newGuard(po prim.Options) (*guard.Guard, error) {
	o := s.opts
	var err error
	s.bindings, err = binding.Open(o.Paths.Bindings, o.Bindings)
	if err != nil {
		return nil, err
	}
	// Standing approvals: config, then the repository's .nodwyn/allow.toml.
	s.allow = guard.NewCommandAllowIn(o.Repo, o.Config.Policy.AllowCommands...)
	files, err := s.trustedProject()
	if err != nil {
		return nil, err
	}
	s.initSource = files.Init
	repoAllow, err := config.ParseRepoAllow(files.Allow)
	if err != nil {
		return nil, err
	}
	for _, c := range repoAllow.Commands {
		s.allow.Add(c)
	}
	s.writes = guard.NewWriteAllow(o.Config.Policy.AllowWrites...)
	for _, w := range repoAllow.Writes {
		s.writes.Add(w)
	}
	// P6 L2: provenance. The checker runs before L1 (and later L3): when
	// L2 knows, the learned layer is not paid for (SPEC ladder principle).
	egressCheckers := []guard.Checker{guard.Taint{Table: s.bindings, Policy: o.Config.Policy.Unbound}}
	if s.keychain != nil {
		egressCheckers = append(egressCheckers, guard.Egress{Table: s.bindings, Policy: o.Config.Policy.Unbound, Recorder: s, Scopes: s.credentialScopes})
	}
	// P4 L1: scan the secret-bearing text of egress-bearing prims for
	// credentials the model wrote directly (not via a credential handle).
	if o.Config.Policy.L1.Enabled {
		sc := l1.New()
		for _, cl := range o.Config.Policy.L1.AllowClasses {
			sc.AllowClass(cl)
		}
		for _, v := range o.Config.Policy.L1.AllowValues {
			sc.AllowValue(v)
		}
		egressCheckers = append(egressCheckers, guard.TextSecret{Table: s.bindings, Scanner: sc})
	}
	egressCheckers = append(egressCheckers, uploadChecker{s})
	checkers := append([]guard.Checker{guard.BindingReload{Policy: s.bindings}}, prim.Checkers(po, s.allow, s.writes, egressCheckers...)...)
	if s.approval().Writes == config.ApproveAllow {
		checkers = append(checkers, guard.FolderWrites{})
	}
	// One guard for the session. While init.scm loads nobody is listening,
	// so confirms are denied; the real confirmer is installed afterwards on
	// the same guard, keeping any checkers init.scm registered.
	g := guard.New(guard.DenyAll, s, checkers...).WithSchemeEval(s.evalScheme).WithCredentialScopes(s.credentialScopes)
	return g, nil
}

// trustedProject takes one snapshot, so a file changed after its trust check
// is never the code or policy loaded by this session.
func (s *Session) trustedProject() (config.ProjectFiles, error) {
	files, err := config.ReadProjectFiles(s.opts.Repo)
	if err != nil {
		return config.ProjectFiles{}, err
	}
	trusted, err := config.ProjectTrusted(s.opts.Paths, s.project(), files)
	if err != nil {
		return config.ProjectFiles{}, err
	}
	if !trusted {
		if files.Present() && s.opts.InitFile == "" {
			s.note("Repository init.scm and allow.toml were skipped: review them with nodwyn trust --repo " + s.project())
		}
		return config.ProjectFiles{}, nil
	}
	return files, nil
}

func (s *Session) loadInit() error {
	ctx := scheme.WithOrigin(context.Background(), scheme.OriginUser)
	before := s.interp.Global().Snapshot()
	if s.opts.InitFile != "" {
		if err := s.interp.LoadFile(ctx, s.opts.InitFile); err != nil {
			return err
		}
	} else if _, err := s.interp.EvalString(ctx, string(s.initSource)); err != nil {
		return fmt.Errorf("core: init: %w", err)
	}
	s.noteInitChanges(before, s.interp.Global().Snapshot())
	return nil
}

// initialize assembles runtime state using today's configuration. It never
// replays evaluations or approvals from the conversation log.
func (s *Session) initialize() (err error) {
	o := s.opts
	// The default loop's profile is fixed with the session, as its prompt and
	// its tools are (ADR 9). A resumed session resolves it again, from the
	// configuration, the environment and the way it is resumed.
	if s.profile, err = o.Config.Loop.ResolveProfile(os.Getenv(config.ProfileEnv), o.unattended()); err != nil {
		return fmt.Errorf("core: %w", err)
	}
	s.procs = prim.NewProcs()
	if o.Config.Experimental.Terminal {
		s.terms = prim.NewTerms()
		s.note("Experimental: the terminal tools (term-open and the rest) are on. They are temporary and may change or go away, and once a terminal is open nothing the model types in it is checked (ADR 48).")
	}
	if s.scratch, err = makeScratch(s.opts.Paths.DataDir, s.id); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			s.closeMCP()
			removeScratch(s.scratch)
		}
	}()
	// The folders outside the root it may use are the person's, named in the
	// config directory for the project (ADR 22): a worktree's are its
	// repository's. A session with no config directory has none.
	registry := ""
	if o.Paths.ConfigDir != "" {
		registry = config.RegistryFile(o.Paths)
	}
	if s.workspace, err = config.LoadWorkspace(registry, s.project()); err != nil {
		return fmt.Errorf("core: %w", err)
	}
	// A task started or resumed in a forgotten folder brings it back to the
	// console's suggestions (ADR 22, amended 2026-10-07). The marker only
	// hides a suggestion, so failing to clear it is noted, not a refusal.
	if err := config.ClearForgotten(registry, s.project()); err != nil {
		s.note("the folder stays forgotten in the console: " + err.Error())
	}
	// The model this conversation talks to: config.toml's [control] model,
	// or the one chosen for it (ADR 40). Every part of the session that
	// asks it (the loop, the window, the planner, the image checks) reads
	// this one, so a switch is the only place it changes.
	if s.model, s.effort, err = resolveChoice(o.Config, o.Model); err != nil {
		return err
	}
	// Interpreter: output becomes an event, never stdout.
	s.interp = scheme.New(scheme.WithOutput(outputWriter{s}), scheme.WithBaseDir(o.Repo))
	po := s.primitiveOptions()
	if o.JudgeApprove && po.Judge == nil {
		return errors.New("core: judged approval needs a judge: a [judge] server whose key is stored, judge.content other than none, and an unlocked keychain")
	}
	if err := prim.Register(s.interp, po); err != nil {
		return err
	}
	g, err := s.newGuard(po)
	if err != nil {
		return err
	}
	s.interp.SetGate(turnGate{Gate: g, s: s})
	s.connectMCP()
	s.interp.SetExpand(expandScratch(s.scratch))
	if err := s.registerHistory(); err != nil {
		return err
	}
	if err := s.registerRecall(); err != nil {
		return err
	}
	if err := s.registerReload(); err != nil {
		return err
	}
	if err := s.registerPlanRequired(); err != nil {
		return err
	}
	// The loop installs agent-turn before init.scm loads, so a project can
	// redefine it; the tool list is frozen once init.scm has run.
	requestTimeout, _ := o.Config.Loop.Durations()
	models := s.models()
	control, win, ceiling, sizing, unknown := s.sizeWindow(models[loop.ControlModel])
	models[loop.ControlModel] = control
	s.contextUnknown = unknown
	if sizing != "" {
		s.note(sizing)
	}
	s.loop, err = loop.New(s, s.interp, loop.Config{
		Window:         window.Policy{HighWater: win.HighWater, LowWater: win.LowWater, MaxResultTokens: win.MaxResultTokens, Thinking: true, InConversation: true, PairTools: true, NamedCuts: true, Blocks: true, WideReads: true},
		Ceiling:        ceiling,
		MaxOutput:      o.Config.Window.OutputReserve,
		Models:         models,
		RequestTimeout: requestTimeout,
		Root:           o.Repo,
		Delegates:      s.delegates,
		Profile:        s.profile,
		Sandbox:        o.Sandbox,
	})
	if err != nil {
		return err
	}
	if err := s.loadInit(); err != nil {
		return err
	}
	// Prompts are Scheme (internal/loop/prompts.scm, or the project's
	// redefinitions); read them once, now that init.scm has run.
	if err := s.loop.ReadPrompts(o.Repo); err != nil {
		return fmt.Errorf("core: %w", err)
	}
	if err := s.loop.Freeze(); err != nil {
		return err
	}
	var confirmer guard.Confirmer = s
	switch {
	case o.NoConfirm:
		confirmer = guard.DenyAll
	case o.AutoApprove:
		confirmer = guard.AutoAllow
	case o.JudgeApprove:
		confirmer = judgeConfirmer{s}
	case o.Unattended:
		confirmer = guard.DenyAll
	default:
		confirmer = s.personConfirmer()
	}
	g.SetConfirmer(confirmer)
	return nil
}
