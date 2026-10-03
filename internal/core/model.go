package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/loop"
	"github.com/fclaude/nodwyn/internal/window"
)

// ModelChoice names the model a conversation talks to (ADR 40): config.toml's
// [control] model, a [models.NAME] entry, or a model on one of the configured
// [servers.NAME]. The zero choice is the [control] model, so a session opened
// without one talks to what config.toml names, unconfigured included.
//
// A choice names a server, never a key: a model on another server is reached
// with that server's key, and its bindings are checked per request as every
// other request is. No choice can reach an address the person has not
// configured.
type ModelChoice struct {
	// Name is a registry name: "control", or a [models.NAME] entry. Set
	// alone.
	Name string
	// Server is a [servers.NAME] and Model the model id on it, as the
	// provider names it. Set together, and not with Name: a model that
	// needs no tuning of its own needs no [models.NAME] block.
	Server string
	Model  string
	// Actor says who chose it, for the log; the daemon's peer.
	Actor string
}

// ModelState is the model a conversation talks to now, as session.info
// reports it and a switch answers with. It names no key and no credential
// class, and no address a person has not configured.
type ModelState struct {
	// Name is the registry name the model has: "control" for config.toml's
	// [control] model, the [models.NAME] entry, or empty for a model on a
	// server that no name in config.toml names.
	Name string `json:"name,omitempty"`
	// Server is the [servers.NAME] the model is on, when it is on one.
	Server string `json:"server,omitempty"`
	// Model is the model id as the provider names it.
	Model string `json:"model"`
}

// Model reports the model this conversation talks to now.
func (s *Session) Model() ModelState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelState()
}

// currentModel is that model itself, for what needs its address or its
// settings: an image's confirm names the model and the host it may go to.
func (s *Session) currentModel() config.Model {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.model
}

func (s *Session) modelState() ModelState {
	c := s.opts.Model
	return ModelState{Name: c.Name, Server: s.model.Server, Model: s.model.Model}
}

// resolveModel turns a choice into the model a conversation talks to. It is
// the one place a choice becomes a config.Model, so opening on one, switching
// to one and restoring one agree. The choice itself is left as the person
// gave it; what it named is read back from the model (its Server) and the
// choice (its Name).
func resolveModel(cfg config.Config, c ModelChoice) (config.Model, error) {
	switch {
	case c.Name != "" && (c.Server != "" || c.Model != ""):
		return config.Model{}, errors.New("give a model by name, or by its server and model, not both")
	case c.Name == loop.ControlModel || (c.Name == "" && c.Server == "" && c.Model == ""):
		// The [control] model, configured or not: a session may open
		// without one, to replay or evaluate, and its first model request
		// says what to add (MissingControl).
		return withServer(cfg, cfg.Control), nil
	case c.Name != "":
		m, ok := cfg.Models[c.Name]
		if !ok {
			return config.Model{}, fmt.Errorf("no model named %q is configured; the models are %s", c.Name, strings.Join(modelNames(cfg), ", "))
		}
		return withServer(cfg, m), nil
	case c.Model == "":
		return config.Model{}, errors.New("a model is required with a server")
	case c.Server == "":
		return config.Model{}, errors.New(`a server is required with a model; give a [servers.NAME] the model is on`)
	default:
		srv, ok := cfg.Servers[c.Server]
		if !ok {
			return config.Model{}, fmt.Errorf("no server named %q is configured; the servers are %s", c.Server, strings.Join(serverNames(cfg), ", "))
		}
		return config.Model{Server: c.Server, Provider: srv.Provider, URL: srv.URL, Credential: srv.Credential, Model: c.Model}, nil
	}
}

// withServer completes a model that names a server: config.Parse copies the
// server's provider, url and credential into every model that names one, so a
// choice resolves the same way whether config.toml was parsed or the config
// was built in memory.
func withServer(cfg config.Config, m config.Model) config.Model {
	srv, ok := cfg.Servers[m.Server]
	if m.Server == "" || !ok {
		return m
	}
	if m.URL == "" {
		m.URL = srv.URL
	}
	if m.Provider == "" {
		m.Provider = srv.Provider
	}
	if m.Credential == "" {
		m.Credential = srv.Credential
	}
	return m
}

// modelNames lists the names a choice may ask, for a refusal that says what
// it could have asked instead.
func modelNames(cfg config.Config) []string {
	names := make([]string, 0, len(cfg.Models)+1)
	names = append(names, loop.ControlModel)
	for name := range cfg.Models {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// serverNames lists the configured servers, sorted.
func serverNames(cfg config.Config) []string {
	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) == 0 {
		return []string{"none configured"}
	}
	return names
}

// SetModel makes c the model this conversation talks to, from the next turn
// on: a person chose another model for it (ADR 40). The conversation's key
// does not change; a model on another server brings that server's key, and
// every request still checks its binding.
//
// It is refused while a turn or an attachment is running, because the turn
// under way has already asked its model and an attachment was confirmed for
// the model it named; when the target is not configured; when the target's
// key is in a keychain this task does not have; and when the conversation
// already holds more than the target's context, less the room its reply cap
// needs.
func (s *Session) SetModel(c ModelChoice) (ModelState, error) {
	done, err := s.beginSwitch()
	if err != nil {
		return ModelState{}, err
	}
	state, err := s.switchModel(c)
	done()
	return state, err
}

// beginSwitch holds the session for one model switch: no turn or attachment
// starts until done is called, and Close waits for it as it waits for an
// attachment. The switch needs that hold because its record cannot be written
// with s.mu held (Record emits, and Emit takes it), while the change it
// announces must not let a turn in between.
func (s *Session) beginSwitch() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed || s.closing:
		return nil, errors.New("core: session is closed")
	case s.busy:
		return nil, errors.New("core: a turn is running; another model applies from the next turn, so switch when this one ends")
	case s.attaching:
		return nil, errors.New("core: an attachment is being added; the model applies from the next turn, so switch when it is done")
	case s.switching:
		return nil, errors.New("core: another switch is in progress; wait until it is done")
	}
	s.switching = true
	s.turns.Add(1)
	return func() {
		s.mu.Lock()
		s.switching = false
		s.mu.Unlock()
		s.turns.Done()
	}, nil
}

// switchModel chooses the model, sizes the window for it, checks the
// conversation fits, records the switch and makes it the one (model-turn)
// asks, in that order. The switch is held (beginSwitch), so no turn reads the
// model while it changes.
func (s *Session) switchModel(c ModelChoice) (ModelState, error) {
	if c.Name == "" && c.Server == "" && c.Model == "" {
		// The zero choice is the [control] model, as it is at open.
		c.Name = loop.ControlModel
	}
	m, err := resolveModel(s.opts.Config, c)
	if err != nil {
		return ModelState{}, err
	}
	if m.URL == "" || m.Model == "" {
		return ModelState{}, errors.New("there is nothing to switch to: config.toml's [control] model has no url or model id; choose a [models.NAME] entry, or a [servers.NAME] and the model on it")
	}
	if m.Credential != "" && s.keychain == nil {
		return ModelState{}, fmt.Errorf("the %s key is in a keychain this task does not have: unlock it, then close and resume the task", m.Credential)
	}
	// The window is what opening the conversation on this model would give
	// it: sized for its context, asked of the server's model list when
	// config.toml does not say it.
	ep, win, ceiling, sizing := s.sizeWindow(s.endpoint(m))
	marks := window.Policy{HighWater: win.HighWater, LowWater: win.LowWater, MaxResultTokens: win.MaxResultTokens}
	if err := s.fitsModel(m, ep, marks); err != nil {
		return ModelState{}, err
	}
	s.mu.Lock()
	from := s.model.Model
	s.mu.Unlock()
	// The record, with the recipe the next requests are built by, goes in
	// before the model changes, so the turn that asks it comes after it in
	// the log and replays by it.
	err = s.loop.SetControl(ep, marks, ceiling, func(recipe *log.WindowContext) error {
		_, err := s.Record(log.KindSessionModel, modelRecord(from, c, m, recipe))
		return err
	})
	if err != nil {
		return ModelState{}, err
	}
	s.mu.Lock()
	s.model, s.opts.Model = m, c
	s.mu.Unlock()
	if sizing != "" {
		s.note(sizing)
	}
	return s.Model(), nil
}

// modelRecord is the session.model record of a conversation talking to m,
// chosen as c, from the model id from.
func modelRecord(from string, c ModelChoice, m config.Model, recipe *log.WindowContext) log.SessionModel {
	return log.SessionModel{From: from, Model: m.Model, Server: m.Server, Name: c.Name, URL: m.URL, Actor: c.Actor, Context: recipe}
}

// fitsModel reports whether the conversation can be carried by model m with
// the endpoint ep: the window built with the water marks sized for it
// (marks) must fit the context m reads, less the room its reply cap needs.
// User messages are never trimmed, so a conversation can be longer than any
// window. A model whose context neither config.toml nor its server's model
// list says is left to its server, and nothing is judged.
func (s *Session) fitsModel(m config.Model, ep loop.Endpoint, marks window.Policy) error {
	if ep.NumCtx <= 0 {
		return nil
	}
	reserve := s.opts.Config.Window.OutputReserve
	if ep.MaxOutput > 0 {
		reserve = ep.MaxOutput
	}
	room := ep.NumCtx - reserve
	if held := s.loop.WindowTokens(marks); held > room {
		return fmt.Errorf("the conversation holds about %d tokens and %s reads %d (num_ctx) less its %d-token reply reserve: compact it first, or choose a model with more context", held, m.Model, ep.NumCtx, reserve)
	}
	return nil
}
