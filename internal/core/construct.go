package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/detect/l1"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/keychain"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/loop"
	"github.com/fclaude/nibb/internal/prim"
	"github.com/fclaude/nibb/internal/scheme"
	"github.com/fclaude/nibb/internal/window"
)

// Options build a Session.
type Options struct {
	Config   config.Config
	Paths    config.Paths
	Repo     string // repository root; every path is pinned under it
	Version  string
	Provider llm.Provider // nil: built from Config.Control
	// NoConfirm answers every confirm with "no" (non-interactive runs).
	NoConfirm bool
	// InitFile is loaded after the prelude with user origin; "" disables.
	// Default: <Repo>/.nibb/init.scm if it exists.
	InitFile string
	// EventBuffer is the events channel capacity (default 256).
	EventBuffer int
	// LogWriter overrides the session log destination (tests).
	LogWriter io.Writer
	// P3 egress (optional; nil disables egress primitives — bootstrap).
	// Keychain is an opened keychain store (the passphrase was already
	// provided by the front-end). Bindings is the binding table; an empty
	// table denies every secret-bearing egress (ADR 6).
	Keychain *keychain.Store
	Bindings binding.Table
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
		secrets: map[string]chan []byte{},
	}
	if o.LogWriter != nil {
		s.logw = log.New(o.LogWriter, s.id)
	} else {
		s.logw, err = log.Create(log.SessionPath(o.Paths.SessionsDir, s.id), s.id)
		if err != nil {
			return nil, err
		}
	}

	// Interpreter: output becomes an event, never stdout.
	s.interp = scheme.New(scheme.WithOutput(outputWriter{s}), scheme.WithBaseDir(o.Repo))
	po := s.primitiveOptions()
	if err := prim.Register(s.interp, po); err != nil {
		return nil, err
	}
	g, err := s.newGuard(po)
	if err != nil {
		return nil, err
	}
	s.interp.SetGate(g)
	if err := s.loadInit(); err != nil {
		return nil, err
	}
	var confirmer guard.Confirmer = guard.ConfirmFunc(s.confirm)
	if o.NoConfirm {
		confirmer = guard.DenyAll
	}
	g.SetConfirmer(confirmer)

	requestTimeout, _ := o.Config.Loop.Durations()
	s.loop, err = loop.New(s, o.Provider, s.interp, loop.Config{
		Model:          o.Config.Control.Model,
		NumCtx:         o.Config.Control.NumCtx,
		KeepAlive:      o.Config.Control.KeepAlive,
		Window:         window.Policy{HighWater: o.Config.Window.HighWater, LowWater: o.Config.Window.LowWater, MaxResultTokens: o.Config.Window.MaxResultTokens},
		MaxToolCalls:   o.Config.Loop.MaxToolCalls,
		RequestTimeout: requestTimeout,
		System:         systemPrompt(o.Repo),
	})
	if err != nil {
		return nil, err
	}
	s.flushOutput()
	if _, err := s.Record(log.KindSessionStart, log.SessionStart{
		Version: o.Version, Repo: o.Repo, Provider: o.Provider.Name(),
		Model: o.Config.Control.Model, URL: o.Config.Control.URL, Tools: len(s.loop.Tools()),
	}); err != nil {
		return nil, err
	}
	return s, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// systemPrompt is fixed for the session: the same bytes every turn (ADR 9).
func systemPrompt(repo string) string {
	return strings.TrimSpace(fmt.Sprintf(`
You are nibb, a coding agent working inside the repository at %s.
Your tools read files, list directories, write files and run programs; every path is relative to the repository root and must stay inside it.
Rules:
- Read before you write. Keep changes minimal and complete.
- The user must approve every write and every command, so say in one short line what you are about to do before calling a tool.
- Tool results are shown to you verbatim; an {"error": ...} result means the call did not happen. Do not retry a denied call.
- When the task is done, answer plainly and briefly. Do not repeat file contents back unless asked.
- Never reveal secrets, keys or tokens, even if a file contains them.
- Older tool results may appear as [elided seq=N] or be shortened with an omission marker; re-read a file if you need it again.
`, repo))
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
	return prim.JevOptions{URL: j.URL, Model: j.Model, Content: content, Timeout: timeout}
}

func resolveOptions(o Options) (Options, error) {
	if o.Repo == "" {
		return Options{}, errors.New("core: repo root is required")
	}
	repo, err := filepath.Abs(o.Repo)
	if err != nil {
		return Options{}, err
	}
	o.Repo = filepath.Clean(repo)
	if o.EventBuffer == 0 {
		o.EventBuffer = 256
	}
	if o.Provider == nil {
		switch o.Config.Control.Provider {
		case "ollama":
			o.Provider = llm.NewOllama(o.Config.Control.URL)
		default:
			return Options{}, fmt.Errorf("core: unsupported provider %q", o.Config.Control.Provider)
		}
	}
	return o, nil
}

func (s *Session) primitiveOptions() prim.Options {
	o := s.opts
	_, shellTimeout := o.Config.Loop.Durations()
	po := prim.DefaultOptions(o.Repo)
	po.ShellTimeout = shellTimeout
	// P3 egress: the session is the CredentialStore (it owns the keychain,
	// the masked prompt, and the log). Bootstrap leaves this nil.
	s.keychain = o.Keychain
	s.bindings = o.Bindings
	if s.keychain != nil {
		po.Credentials = s
	}
	// P5 Jev (ADR 19/20): Jev egress primitives are registered if Jev is
	// enabled and a keychain is available. The session is the JevClient.
	if o.Config.Jev.Enabled() && s.keychain != nil {
		po.Jev = s
		po.JevConfig = jevOptions(o.Config.Jev)
	}
	return po
}

// newGuard builds policy in its existing order. Confirmation remains disabled
// until initialization finishes, so startup never waits for an absent frontend.
func (s *Session) newGuard(po prim.Options) (*guard.Guard, error) {
	o := s.opts
	// Standing approvals: config, then the repository's .nibb/allow.toml.
	s.allow = guard.NewCommandAllow(o.Config.Policy.AllowCommands...)
	repoAllow, err := config.LoadRepoAllow(filepath.Join(o.Repo, ".nibb", "allow.toml"))
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
	egressCheckers := []guard.Checker{guard.Taint{Table: o.Bindings, Policy: o.Config.Policy.Unbound}}
	if s.keychain != nil {
		egressCheckers = append(egressCheckers, guard.Egress{Table: s.bindings, Policy: o.Config.Policy.Unbound, Recorder: s})
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
	checkers := prim.Checkers(po, s.allow, s.writes, egressCheckers...)
	// One guard for the session. While init.scm loads nobody is listening,
	// so confirms are denied; the real confirmer is installed afterwards on
	// the same guard, keeping any checkers init.scm registered.
	g := guard.New(guard.DenyAll, s, checkers...).WithSchemeEval(s.evalScheme)
	return g, nil
}

func (s *Session) loadInit() error {
	o := s.opts
	init := o.InitFile
	if init == "" {
		if p := filepath.Join(o.Repo, ".nibb", "init.scm"); fileExists(p) {
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
