// Package core is the UI-agnostic façade over the harness (ADR 10). A
// Session owns the interpreter, the guard, the log and the loop; a
// front-end drives it with Send/Answer/Abort and watches Events. Nothing in
// here knows what a terminal is.
package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/config"
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

// Session is one conversation with one log.
type Session struct {
	opts   Options
	id     string
	logw   *log.Writer
	events []log.Event
	evmu   sync.Mutex
	out    chan event.Event
	interp *scheme.Interp
	loop   *loop.Loop

	mu         sync.Mutex
	busy       bool
	turnCancel context.CancelFunc
	closed     bool

	prompts  map[string]chan bool
	pending  map[string]guard.Prompt
	promptN  int
	promptMu sync.Mutex

	allow  *guard.CommandAllow
	writes *guard.WriteAllow
	// turnTrust: writes are approved without a prompt until the turn ends
	// ("t" at a write prompt). Reset when the turn goes idle.
	turnTrust bool

	// P3 egress.
	keychain *keychain.Store
	bindings binding.Table

	// P3 credential prompts: pending masked-secret prompts.
	secrets   map[string]chan []byte
	secretsMu sync.Mutex
}

// New creates a session, writes session.start, and loads the init file.
func New(o Options) (*Session, error) {
	if o.Repo == "" {
		return nil, errors.New("core: repo root is required")
	}
	repo, err := filepath.Abs(o.Repo)
	if err != nil {
		return nil, err
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
			return nil, fmt.Errorf("core: unsupported provider %q", o.Config.Control.Provider)
		}
	}
	s := &Session{
		opts:    o,
		id:      log.NewSessionID(time.Now()),
		out:     make(chan event.Event, o.EventBuffer),
		prompts: map[string]chan bool{},
		pending: map[string]guard.Prompt{},
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
	if err := prim.Register(s.interp, po); err != nil {
		return nil, err
	}
	var confirmer guard.Confirmer = guard.ConfirmFunc(s.confirm)
	if o.NoConfirm {
		confirmer = guard.DenyAll
	}
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
	var egressCheckers []guard.Checker
	if s.keychain != nil {
		egressCheckers = append(egressCheckers, guard.Egress{Table: s.bindings, Policy: o.Config.Policy.Unbound, Recorder: s})
	}
	checkers := prim.Checkers(po, s.allow, s.writes, egressCheckers...)
	// Init-time evaluation cannot prompt (nobody is listening yet): tools
	// may be registered, actions are denied.
	s.interp.SetGate(guard.New(guard.DenyAll, s, checkers...))
	init := o.InitFile
	if init == "" {
		if p := filepath.Join(o.Repo, ".nibb", "init.scm"); fileExists(p) {
			init = p
		}
	}
	if init != "" {
		if err := s.interp.LoadFile(scheme.WithOrigin(context.Background(), scheme.OriginUser), init); err != nil {
			return nil, fmt.Errorf("core: init: %w", err)
		}
	}
	s.interp.SetGate(guard.New(confirmer, s, checkers...))

	requestTimeout, _ := o.Config.Loop.Durations()
	s.loop = loop.New(s, o.Provider, s.interp, loop.Config{
		Model:          o.Config.Control.Model,
		NumCtx:         o.Config.Control.NumCtx,
		KeepAlive:      o.Config.Control.KeepAlive,
		Window:         window.Policy{HighWater: o.Config.Window.HighWater, LowWater: o.Config.Window.LowWater, MaxResultTokens: o.Config.Window.MaxResultTokens},
		MaxToolCalls:   o.Config.Loop.MaxToolCalls,
		RequestTimeout: requestTimeout,
		System:         systemPrompt(o.Repo),
	})
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

// ID is the session id (also the log file's base name).
func (s *Session) ID() string { return s.id }

// Repo is the pinned repository root.
func (s *Session) Repo() string { return s.opts.Repo }

// Tools lists the tools offered to the model.
func (s *Session) Tools() []llm.Tool { return s.loop.Tools() }

// Events is the stream a front-end reads. It is closed by Close.
func (s *Session) Events() <-chan event.Event { return s.out }

// Send starts a turn for the user's text. It returns immediately; the turn
// runs in the background and reports through Events, ending with a status
// event whose State is "idle".
func (s *Session) Send(text string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("core: session is closed")
	}
	if s.busy {
		s.mu.Unlock()
		return errors.New("core: a turn is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.busy = true
	s.turnCancel = cancel
	s.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			s.busy = false
			s.turnCancel = nil
			s.turnTrust = false
			s.mu.Unlock()
			s.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "idle"}})
		}()
		if err := s.loop.Turn(ctx, text); err != nil {
			s.Emit(event.Event{Kind: event.Error, Payload: event.ErrorInfo{Err: err.Error()}})
		}
	}()
	return nil
}

// Busy reports whether a turn is running.
func (s *Session) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// Abort cancels the running turn, if any: the model request is dropped, a
// running tool is killed, a pending confirm is answered "no".
func (s *Session) Abort() {
	s.mu.Lock()
	cancel := s.turnCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Answer replies to a prompt event. "y"/"yes" confirm once. "a"/"always"
// confirms and adds a standing approval for the rest of the session: the
// command prefix for a command line, the file's directory for a write.
// "t"/"turn" (writes only) confirms and approves every further write until
// the turn ends. Anything else declines.
func (s *Session) Answer(id, reply string) error {
	s.promptMu.Lock()
	ch, ok := s.prompts[id]
	p := s.pending[id]
	if ok {
		delete(s.prompts, id)
		delete(s.pending, id)
	}
	s.promptMu.Unlock()
	if !ok {
		return fmt.Errorf("core: no pending prompt %q", id)
	}
	r := strings.ToLower(strings.TrimSpace(reply))
	switch {
	case (r == "a" || r == "always") && len(p.Argv) > 0:
		prefix := guard.Prefix(p.Argv)
		s.allow.Add(prefix)
		_, _ = s.Record(log.KindNote, log.Note{Text: "standing command approval for this session: " + prefix})
		ch <- true
	case (r == "a" || r == "always") && p.Path != "":
		prefix := guard.DirPrefix(p.Path)
		if guard.IsProtected(p.Path) {
			_, _ = s.Record(log.KindNote, log.Note{Text: "no standing approval for protected path " + p.Path + "; approved once"})
		} else {
			s.writes.Add(prefix)
			_, _ = s.Record(log.KindNote, log.Note{Text: "standing write approval for this session: " + prefix})
		}
		ch <- true
	case (r == "t" || r == "turn") && p.Path != "":
		s.mu.Lock()
		s.turnTrust = true
		s.mu.Unlock()
		_, _ = s.Record(log.KindNote, log.Note{Text: "writes approved for the rest of this turn"})
		ch <- true
	default:
		ch <- r == "y" || r == "yes"
	}
	return nil
}

// Allowed lists the standing command approvals in effect.
func (s *Session) Allowed() []string { return s.allow.List() }

// AllowedWrites lists the standing write approvals in effect.
func (s *Session) AllowedWrites() []string { return s.writes.List() }

// confirm is the guard's Confirmer: emit a prompt, wait for Answer or
// cancellation.
func (s *Session) confirm(ctx context.Context, p guard.Prompt) (bool, error) {
	if p.Path != "" && !guard.IsProtected(p.Path) {
		s.mu.Lock()
		trust := s.turnTrust
		s.mu.Unlock()
		if trust {
			_, _ = s.Record(log.KindNote, log.Note{Text: "write approved by turn trust: " + p.Prim + " " + p.Path + "\n" + p.Detail})
			return true, nil
		}
	}
	s.promptMu.Lock()
	s.promptN++
	id := fmt.Sprintf("p%d", s.promptN)
	ch := make(chan bool, 1)
	s.prompts[id] = ch
	s.pending[id] = p
	s.promptMu.Unlock()
	s.Emit(event.Event{Kind: event.PromptConfirm, Payload: event.Confirm{ID: id, Prompt: p}})
	select {
	case ok := <-ch:
		return ok, nil
	case <-ctx.Done():
		s.promptMu.Lock()
		delete(s.prompts, id)
		delete(s.pending, id)
		s.promptMu.Unlock()
		return false, ctx.Err()
	}
}

// Eval evaluates Scheme with user origin (a front-end command). Output is
// delivered as Output events; the result is returned.
func (s *Session) Eval(ctx context.Context, src string) (scheme.Value, error) {
	return s.interp.EvalString(scheme.WithOrigin(ctx, scheme.OriginUser), src)
}

// Record appends to the log, keeps the event for the window, and forwards
// it to the front-end. It is the loop.Host implementation.
func (s *Session) Record(kind log.Kind, payload any) (log.Event, error) {
	ev, err := s.logw.Append(kind, payload)
	if err != nil {
		return ev, err
	}
	s.evmu.Lock()
	s.events = append(s.events, ev)
	s.evmu.Unlock()
	s.Emit(event.FromLog(ev, payload))
	return ev, nil
}

// Events returns a snapshot of the log so far (loop.Host).
func (s *Session) EventsSoFar() []log.Event {
	s.evmu.Lock()
	defer s.evmu.Unlock()
	return append([]log.Event(nil), s.events...)
}

// Emit delivers an event to the front-end (loop.Host). It blocks if the
// front-end is not draining; that is backpressure, not a bug.
func (s *Session) Emit(ev event.Event) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	s.out <- ev
}

// Close ends the session: aborts a running turn, writes session.end, closes
// the log and the events channel.
func (s *Session) Close() error {
	s.Abort()
	// Wait for the turn goroutine to finish so it does not emit after close.
	for s.Busy() {
		time.Sleep(5 * time.Millisecond)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	_, _ = s.logw.Append(log.KindSessionEnd, log.SessionEnd{Reason: "closed"})
	err := s.logw.Close()
	close(s.out)
	return err
}

// outputWriter turns Scheme display/write output into Output events.
type outputWriter struct{ s *Session }

func (w outputWriter) Write(p []byte) (int, error) {
	w.s.Emit(event.Event{Kind: event.Output, Payload: event.Text{Text: string(p)}})
	return len(p), nil
}

// --- P3 egress: Session implements prim.CredentialStore -----------------

// PromptAndAdd asks the user (masked input) for a secret of the given class
// and stores it in the keychain. It returns the opaque handle. The secret
// value never becomes a Scheme value (ADR 5).
func (s *Session) PromptAndAdd(ctx context.Context, class string) (*scheme.Handle, error) {
	if s.keychain == nil {
		return nil, errors.New("core: no keychain configured")
	}
	// Emit a secret prompt event and wait for the front-end to reply.
	s.promptMu.Lock()
	s.promptN++
	id := fmt.Sprintf("s%d", s.promptN)
	ch := make(chan []byte, 1)
	s.promptMu.Unlock()

	// Store the pending secret prompt so SecretReply can find it.
	s.secretsMu.Lock()
	s.secrets[id] = ch
	s.secretsMu.Unlock()

	s.Emit(event.Event{Kind: event.PromptSecret, Payload: event.SecretPrompt{ID: id, Class: class}})

	select {
	case secret := <-ch:
		if len(secret) == 0 {
			return nil, errors.New("core: secret was empty or declined")
		}
		h, err := s.keychain.Add(class, secret)
		if err != nil {
			return nil, err
		}
		_, _ = s.Record(log.KindCredRegister, log.CredRegister{Class: class, Handle: h.ID})
		return h, nil
	case <-ctx.Done():
		s.secretsMu.Lock()
		delete(s.secrets, id)
		s.secretsMu.Unlock()
		return nil, ctx.Err()
	}
}

// InjectHeader implements prim.CredentialStore.
func (s *Session) InjectHeader(id, name string, req *http.Request) error {
	if s.keychain == nil {
		return errors.New("core: no keychain configured")
	}
	return s.keychain.InjectHeader(id, name, req)
}

// InjectQuery implements prim.CredentialStore.
func (s *Session) InjectQuery(id, key string, req *http.Request) error {
	if s.keychain == nil {
		return errors.New("core: no keychain configured")
	}
	return s.keychain.InjectQuery(id, key, req)
}

// InjectBody implements prim.CredentialStore.
func (s *Session) InjectBody(id string, req *http.Request) error {
	if s.keychain == nil {
		return errors.New("core: no keychain configured")
	}
	return s.keychain.InjectBody(id, req)
}

// Do implements prim.CredentialStore: sends the HTTP request. The request
// carries the turn's context; the client adds a bound on a silent server.
func (s *Session) Do(req *http.Request) (*http.Response, error) {
	return egressClient.Do(req)
}

var egressClient = &http.Client{Timeout: 2 * time.Minute}

// SecretReply delivers a masked secret to a pending PromptAndAdd.
func (s *Session) SecretReply(id string, secret []byte) error {
	s.secretsMu.Lock()
	ch, ok := s.secrets[id]
	if ok {
		delete(s.secrets, id)
	}
	s.secretsMu.Unlock()
	if !ok {
		return fmt.Errorf("core: no pending secret prompt %q", id)
	}
	ch <- secret
	return nil
}
