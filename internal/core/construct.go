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
	// JudgeApprove has the judge (Jev) answer ordinary confirms, decided in
	// Go (judge.go) so no Scheme can approve a call; security confirms are
	// declined without asking, and a failure is a no. It needs a judge: Jev enabled
	// and a jev-key stored (ADR 18 §5, ADR 20 Seam C).
	JudgeApprove bool
	// Unattended means nobody is there to answer anything (an unattended
	// run, ADR 18 §5): no prompt ever waits for a person. Tool confirms are
	// declined, or answered as AutoApprove answers them when it is set too;
	// a soft budget checkpoint continues up to the hard limit, and both are
	// recorded as answered by "unattended".
	Unattended bool
	// InitFile is loaded after the prelude with user origin; "" disables.
	// Default: <Repo>/.nodwyn/init.scm if it exists.
	InitFile string
	// Model is the model this conversation talks to (ADR 40). The zero
	// choice is config.toml's [control] model. A session resumed from a log
	// that switched its model takes that choice back, when config.toml
	// still has it.
	Model ModelChoice
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
		opts:    o,
		id:      log.NewSessionID(time.Now()),
		out:     make(chan event.Event, o.EventBuffer),
		prompts: map[string]pendingConfirmation{},
		secrets: map[string]pendingSecret{},
	}
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
	})
	if c := s.opts.Model; err == nil && c.Name != loop.ControlModel {
		// A conversation opened on a chosen model records the choice as a
		// switch does, so resuming it talks to that model again (ADR 40).
		_, err = s.Record(log.KindSessionModel, modelRecord("", c, s.model, recipe))
	}
	if err != nil {
		s.closeMCP()
		s.logw.Close()
		removeScratch(s.scratch)
		return nil, err
	}
	s.markStarted()
	return s, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// jevOptions resolves the existing per-session defaults before registration.
func jevOptions(j config.Jev) prim.JevOptions {
	timeout, err := time.ParseDuration(j.Timeout)
	if err != nil {
		timeout = 10 * time.Second
	}
	content := j.Content
	if content == "" {
		content = config.JevContentPlan
	}
	return prim.JevOptions{URL: j.URL, Model: j.Model, Content: content, Timeout: timeout, PriceInput: j.PriceInput, PriceOutput: j.PriceOutput}
}

func resolveOptions(o Options) (Options, error) {
	if _, err := o.Config.Budget.Limits(o.Config.Loop.MaxToolCalls); err != nil {
		return Options{}, err
	}
	// The choice is resolved once, here, so a refusal says what is wrong
	// before a session exists; initialize resolves it into the model the
	// conversation talks to. config.toml's [control] model stays where it
	// is, so a conversation can be moved back to it (ADR 40).
	if _, err := resolveModel(o.Config, o.Model); err != nil {
		return Options{}, err
	}
	if o.Model.Name == "" && o.Model.Server == "" && o.Model.Model == "" {
		o.Model.Name = loop.ControlModel
	}
	if o.Repo == "" {
		return Options{}, errors.New("core: repo root is required")
	}
	repo, err := filepath.Abs(o.Repo)
	if err != nil {
		return Options{}, err
	}
	o.Repo = filepath.Clean(repo)
	if _, err := config.MigrateFolder(o.Repo); err != nil {
		return Options{}, fmt.Errorf("core: moving .%s to %s: %w", config.LegacyName, config.FolderDir, err)
	}
	if o.EventBuffer == 0 {
		o.EventBuffer = 256
	}
	return o, nil
}

func (s *Session) primitiveOptions() prim.Options {
	o := s.opts
	_, shellTimeout := o.Config.Loop.Durations()
	po := prim.DefaultOptions(o.Repo)
	po.Scratch = s.scratch
	po.Private = []string{o.Paths.ConfigDir, o.Paths.DataDir}
	po.Procs = s.procs
	po.NotesFile = notesFile(o.Paths.DataDir, o.Repo)
	po.Kind = s.workspace.Kind
	for _, m := range s.workspace.Mounts {
		po.Mounts = append(po.Mounts, guard.Mount{Path: m.Path, Write: m.Access == "write"})
	}
	po.Versions = &prim.Versions{Dir: prim.VersionsDir(o.Repo), Skip: prim.InGitWorkTree(o.Repo), Record: func(v prim.Version) {
		_, _ = s.Record(log.KindFileVersion, log.FileVersion{Path: v.Path, Hash: v.Hash, Bytes: v.Bytes})
	}}
	// A ranged fs-read stays under the window's cut for one tool result
	// (window.Estimate is bytes/3), with room for its header.
	po.MaxRangeBytes = o.Config.Window.MaxResultTokens*3 - 256
	po.BindingsPath = o.Paths.Bindings
	po.ShellTimeout = shellTimeout
	// P3 egress: the session is the CredentialStore (it owns the keychain,
	// the masked prompt, and the log). Bootstrap leaves this nil.
	s.keychain = o.Keychain
	if s.keychain != nil {
		po.Credentials = s
	}
	// P5 Jev (ADR 19/20): Jev egress primitives are registered if Jev is
	// enabled and a keychain is available. The session is the JevClient.
	// The model's judge-* tools also need a jev-key stored now, since the
	// tool list is fixed for the session, and the "full" content level,
	// since the model chooses what it sends.
	if o.Config.Jev.Enabled() && s.keychain != nil {
		po.Jev = s
		po.JevConfig = jevOptions(o.Config.Jev)
		po.JevConfig.Tools = po.JevConfig.Content == config.JevContentFull && s.HandleForClass(prim.JevClass) != nil
	}
	// W5 documents: google-doc-upload is offered only when a credential of
	// the configured class is stored now, since the tool list is fixed for
	// the session. The session is the DocsClient.
	// Uploads set to deny are not offered at all (uploads.go).
	if class := o.Config.Docs.GoogleCredential; class != "" && s.HandleForClass(class) != nil && s.uploadPolicy() != config.UploadDeny {
		po.Docs = s
		po.DocsConfig = prim.DocsOptions{GoogleClass: class, GoogleUploadURL: o.Config.Docs.GoogleUploadURL}
	}
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
	s.allow = guard.NewCommandAllow(o.Config.Policy.AllowCommands...)
	repoAllow, err := config.LoadRepoAllow(filepath.Join(o.Repo, ".nodwyn", "allow.toml"))
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

func (s *Session) loadInit() error {
	o := s.opts
	init := o.InitFile
	if init == "" {
		if p := filepath.Join(o.Repo, ".nodwyn", "init.scm"); fileExists(p) {
			init = p
		}
	}
	if init != "" {
		if err := s.interp.LoadFile(scheme.WithOrigin(context.Background(), scheme.OriginUser), init); err != nil {
			return fmt.Errorf("core: init: %w", err)
		}
	}
	return nil
}

// initialize assembles runtime state using today's configuration. It never
// replays evaluations or approvals from the conversation log.
func (s *Session) initialize() (err error) {
	o := s.opts
	s.procs = prim.NewProcs()
	if s.scratch, err = makeScratch(s.opts.Paths.DataDir, s.id); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			s.closeMCP()
			removeScratch(s.scratch)
		}
	}()
	if s.workspace, err = config.LoadWorkspace(o.Repo); err != nil {
		return fmt.Errorf("core: %w", err)
	}
	// The model this conversation talks to: config.toml's [control] model,
	// or the one chosen for it (ADR 40). Every part of the session that
	// asks it (the loop, the window, the planner, the image checks) reads
	// this one, so a switch is the only place it changes.
	if s.model, err = resolveModel(o.Config, o.Model); err != nil {
		return err
	}
	// Interpreter: output becomes an event, never stdout.
	s.interp = scheme.New(scheme.WithOutput(outputWriter{s}), scheme.WithBaseDir(o.Repo))
	po := s.primitiveOptions()
	if o.JudgeApprove && (po.Jev == nil || s.HandleForClass(prim.JevClass) == nil) {
		return errors.New("core: judged approval needs a judge: jev.content other than none, an unlocked keychain and a jev-key stored")
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
	control, win, ceiling, sizing := s.sizeWindow(models[loop.ControlModel])
	models[loop.ControlModel] = control
	if sizing != "" {
		s.note(sizing)
	}
	s.loop, err = loop.New(s, s.interp, loop.Config{
		Window:         window.Policy{HighWater: win.HighWater, LowWater: win.LowWater, MaxResultTokens: win.MaxResultTokens, Thinking: true},
		Ceiling:        ceiling,
		MaxOutput:      o.Config.Window.OutputReserve,
		Models:         models,
		RequestTimeout: requestTimeout,
		Root:           o.Repo,
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
