package core

import (
	"errors"
	"fmt"
	"reflect"
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
	// Effort is the reasoning effort its requests ask for (ADR 41): one the
	// model's config.toml entry declares. Empty is the model's own default
	// effort (its `effort` setting), which is none when it sets none, and
	// leaves the model to its provider.
	Effort string
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
	// Effort is the reasoning effort its requests ask for; empty asks none,
	// which leaves the model to its provider (ADR 41).
	Effort string `json:"effort,omitempty"`
	// Efforts are the efforts the model takes, as config.toml declares them;
	// none when it takes no effort setting, so a client offers no choice.
	Efforts []string `json:"efforts,omitempty"`
	// Queued is the choice made while a turn was running, which applies when
	// that turn ends; the turn under way keeps the model and effort above.
	Queued *ModelState `json:"queued,omitempty"`
	// Unavailable says the model this conversation was left on can no longer
	// be reached as it was chosen: config.toml no longer offers it, or no
	// longer offers its effort. The conversation opens, but no turn is asked
	// until a model is chosen for it, so it never talks to another one
	// unasked. Model above is then only what the session was built on.
	Unavailable string `json:"unavailable,omitempty"`
	// Notice says why the last choice made while a turn ran could not be
	// applied when it ended; it is cleared by the next choice.
	Notice string `json:"notice,omitempty"`
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
	state := ModelState{Name: c.Name, Server: s.model.Server, Model: s.model.Model, Effort: s.effort, Efforts: s.opts.Config.Efforts(s.model), Unavailable: s.modelLost, Notice: s.modelNotice}
	if s.queued != nil {
		// Validated when it was made; a config that has changed since only
		// leaves the name, and applying it says why it cannot be.
		q := ModelState{Name: s.queued.Name, Server: s.queued.Server, Model: s.queued.Model, Effort: s.queued.Effort}
		if m, effort, err := resolveChoice(s.opts.Config, *s.queued); err == nil {
			q.Server, q.Model, q.Effort = m.Server, m.Model, effort
		}
		state.Queued = &q
	}
	return state
}

// modelAvailable refuses the turns of a conversation whose model cannot be
// reached as it was chosen (ModelState.Unavailable): it is told to choose
// one, rather than being answered by a model nobody picked for it.
func (s *Session) modelAvailable() error {
	s.mu.Lock()
	lost := s.modelLost
	s.mu.Unlock()
	if lost != "" {
		return fmt.Errorf("this conversation's model is not available (%s); choose a model for it, then send again", lost)
	}
	return nil
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
		// says what to add (MissingControl). Its settings are those of the
		// model's own entry, which it names (config.Tuned).
		return cfg.Tuned(cfg.Resolve(cfg.Control)), nil
	case c.Name != "":
		m, ok := cfg.Models[c.Name]
		if !ok {
			return config.Model{}, fmt.Errorf("no model named %q is configured; the models are %s", c.Name, strings.Join(modelNames(cfg), ", "))
		}
		return cfg.Resolve(m), nil
	case c.Model == "":
		return config.Model{}, errors.New("a model is required with a server")
	case c.Server == "":
		return config.Model{}, errors.New(`a server is required with a model; give a [servers.NAME] the model is on`)
	default:
		srv, ok := cfg.Servers[c.Server]
		if !ok {
			return config.Model{}, fmt.Errorf("no server named %q is configured; the servers are %s", c.Server, strings.Join(serverNames(cfg), ", "))
		}
		return cfg.Tuned(config.Model{Server: c.Server, Provider: srv.Provider, Dialect: srv.Dialect, URL: srv.URL, Credential: srv.Credential, Model: c.Model}), nil
	}
}

// resolveChoice is resolveModel and the effort the choice asks of the model it
// names: the one chosen when it is one the model takes, else the model's own
// default. An effort the model does not take is refused, never dropped, so a
// person who asked for one is not answered with another.
func resolveChoice(cfg config.Config, c ModelChoice) (config.Model, string, error) {
	m, err := resolveModel(cfg, c)
	if err != nil {
		return config.Model{}, "", err
	}
	if c.Effort == "" {
		return m, m.Effort, nil
	}
	efforts := cfg.Efforts(m)
	switch {
	case len(efforts) == 0:
		return config.Model{}, "", fmt.Errorf("%s takes no effort setting: declare efforts = [...] for it in config.toml, naming the words its server accepts", m.Model)
	case !slices.Contains(efforts, c.Effort):
		return config.Model{}, "", fmt.Errorf("%s does not take the effort %q; it takes %s", m.Model, c.Effort, strings.Join(efforts, ", "))
	}
	return m, c.Effort, nil
}

// Choose says what a choice would talk to under cfg: the model, and the effort
// its requests would ask for, or why the choice cannot be had. A front-end
// asks it before a conversation exists, to refuse a choice with the words a
// switch would use, or to see whose key the model needs.
func Choose(cfg config.Config, c ModelChoice) (config.Model, string, error) {
	return resolveChoice(cfg, c)
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

// SetModel makes c the model this conversation talks to, and the effort its
// requests ask for, from the next turn on: a person chose another model, or
// another effort of the same one, for it (ADR 40, ADR 41). The conversation's
// key does not change; a model on another server brings that server's key, and
// every request still checks its binding.
//
// While a turn is running the choice is queued, not refused: the turn under way
// has already asked its model, so the choice applies when it ends, before the
// next turn can start, and the state it answers with says so (Queued). A later
// choice replaces an earlier one that has not applied yet. It is refused while
// an attachment is running, since the attachment was confirmed for the model it
// named; when the target is not configured or does not take the effort; when
// the target's key is in a keychain this task does not have; and, once it
// applies, when the conversation holds more than the target's context less the
// room its reply cap needs: the person is told in the state (Notice) and the
// log (a note), and the conversation keeps the model it had.
func (s *Session) SetModel(c ModelChoice) (ModelState, error) {
	if c.Name == "" && c.Server == "" && c.Model == "" {
		// The zero choice is the [control] model, as it is at open.
		c.Name = loop.ControlModel
	}
	// One choice at a time, and none while a turn ends: the turn's end applies
	// the queue under the same lock (startTurn), so a choice is queued for a
	// turn that is still running, or made between turns, never lost between.
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	m, effort, err := resolveChoice(s.opts.Config, c)
	if err != nil {
		return ModelState{}, err
	}
	if err := s.reachable(m); err != nil {
		return ModelState{}, err
	}
	done, busy, err := s.beginSwitch()
	if err != nil {
		return ModelState{}, err
	}
	if busy {
		return s.queueModel(c, m, effort)
	}
	state, err := s.switchModel(c, m, effort)
	done()
	return state, err
}

// reachable says whether a conversation can ask m at all: it names a model,
// and its key, if it has one, is in a keychain this task has.
func (s *Session) reachable(m config.Model) error {
	if m.URL == "" || m.Model == "" {
		return errors.New("there is nothing to switch to: config.toml's [control] model has no url or model id; choose a [models.NAME] entry, or a [servers.NAME] and the model on it")
	}
	if m.Credential != "" && s.keychain == nil {
		return fmt.Errorf("the %s key is in a keychain this task does not have: unlock it, then close and resume the task", m.Credential)
	}
	return nil
}

// beginSwitch holds the session for one model switch: no turn or attachment
// starts until done is called, and Close waits for it as it waits for an
// attachment. The switch needs that hold because its record cannot be written
// with s.mu held (Record emits, and Emit takes it), while the change it
// announces must not let a turn in between. A running turn holds nothing: busy
// says the choice is to be queued for its end instead.
func (s *Session) beginSwitch() (done func(), busy bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed || s.closing:
		return nil, false, errors.New("core: session is closed")
	case s.busy:
		return nil, true, nil
	case s.attaching:
		return nil, false, errors.New("core: an attachment is being added; the model applies from the next turn, so switch when it is done")
	case s.switching:
		return nil, false, errors.New("core: another switch is in progress; wait until it is done")
	}
	s.switching = true
	s.turns.Add(1)
	return func() {
		s.mu.Lock()
		s.switching = false
		s.mu.Unlock()
		s.turns.Done()
	}, false, nil
}

// queueModel keeps a choice made while a turn runs. It is in the log at once,
// without a window recipe, so a conversation resumed before the turn ends
// still has it; applyQueued writes the record that makes it the model. The
// caller holds modelMu, so the turn's end waits for this to finish.
func (s *Session) queueModel(c ModelChoice, m config.Model, effort string) (ModelState, error) {
	s.mu.Lock()
	from := s.model.Model
	s.mu.Unlock()
	rec := modelRecord(from, c, m, effort, nil)
	rec.Queued = true
	if _, err := s.Record(log.KindSessionModel, rec); err != nil {
		return ModelState{}, err
	}
	s.mu.Lock()
	s.queued, s.modelNotice = &c, ""
	s.mu.Unlock()
	return s.Model(), nil
}

// applyQueued makes the choice queued while a turn ran the model, as the turn
// ends and before the next can start. A choice that cannot be applied (the
// conversation outgrew the target's context, its key is no longer reachable,
// config.toml no longer offers it) is not silently replaced by another: the
// conversation keeps what it had, and the log and the state say why.
func (s *Session) applyQueued(c ModelChoice) {
	// Whatever happens, the choice is no longer queued when this returns.
	defer func() {
		s.mu.Lock()
		s.queued = nil
		s.mu.Unlock()
	}()
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		// The task is being closed: the queued record in the log is the
		// choice, and resuming applies it.
		return
	}
	fail := func(err error) {
		why := "the model chosen while the response ran could not be applied: " + err.Error()
		s.note(why)
		s.mu.Lock()
		s.modelNotice, s.queued = err.Error(), nil
		s.mu.Unlock()
		// The log's last record says what the next turn asks again.
		s.reassertModel()
	}
	m, effort, err := resolveChoice(s.opts.Config, c)
	if err == nil {
		err = s.reachable(m)
	}
	if err != nil {
		fail(err)
		return
	}
	if _, err := s.switchModel(c, m, effort); err != nil {
		fail(err)
	}
}

// reassertModel records the model the conversation talks to as the choice
// again, after a queued one that could not be applied.
func (s *Session) reassertModel() {
	s.mu.Lock()
	m, c, effort := s.model, s.opts.Model, s.effort
	s.mu.Unlock()
	_, _ = s.Record(log.KindSessionModel, modelRecord(m.Model, c, m, effort, s.loop.WindowContext()))
}

// switchModel chooses the model, sizes the window for it, checks the
// conversation fits, records the switch and makes it the one (model-turn)
// asks, in that order. No turn reads the model while it changes: the switch is
// held (beginSwitch), or the turn that ends is still the session's.
func (s *Session) switchModel(c ModelChoice, m config.Model, effort string) (ModelState, error) {
	s.mu.Lock()
	same := reflect.DeepEqual(m, s.model) && s.modelLost == ""
	from := s.model.Model
	s.mu.Unlock()
	if same {
		// Only the effort changes: the window, the key and the address are
		// what they were, so nothing is sized or fitted again.
		err := s.loop.SetEffort(effort, func(recipe *log.WindowContext) error {
			_, err := s.Record(log.KindSessionModel, modelRecord(from, c, m, effort, recipe))
			return err
		})
		if err != nil {
			return ModelState{}, err
		}
		s.mu.Lock()
		s.opts.Model, s.effort, s.modelNotice, s.queued = c, effort, "", nil
		s.mu.Unlock()
		return s.Model(), nil
	}
	// The window is what opening the conversation on this model would give
	// it: sized for its context, asked of the server's model list when
	// config.toml does not say it.
	ep, win, ceiling, sizing := s.sizeWindow(s.endpoint(m, effort))
	marks := window.Policy{HighWater: win.HighWater, LowWater: win.LowWater, MaxResultTokens: win.MaxResultTokens}
	if err := s.fitsModel(m, ep, marks); err != nil {
		return ModelState{}, err
	}
	// The record, with the recipe the next requests are built by, goes in
	// before the model changes, so the turn that asks it comes after it in
	// the log and replays by it.
	err := s.loop.SetControl(ep, marks, ceiling, func(recipe *log.WindowContext) error {
		_, err := s.Record(log.KindSessionModel, modelRecord(from, c, m, effort, recipe))
		return err
	})
	if err != nil {
		return ModelState{}, err
	}
	s.mu.Lock()
	s.model, s.opts.Model, s.effort, s.modelLost, s.modelNotice, s.queued = m, c, effort, "", "", nil
	s.mu.Unlock()
	if sizing != "" {
		s.note(sizing)
	}
	return s.Model(), nil
}

// modelRecord is the session.model record of a conversation talking to m with
// effort, chosen as c, from the model id from.
func modelRecord(from string, c ModelChoice, m config.Model, effort string, recipe *log.WindowContext) log.SessionModel {
	return log.SessionModel{From: from, Model: m.Model, Server: m.Server, Name: c.Name, URL: m.URL, Effort: effort, Actor: c.Actor, Context: recipe}
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
	reserve := s.opts.Config.Window.ReplyCap(ep.NumCtx, ep.MaxOutput)
	room := ep.NumCtx - reserve
	if held := s.loop.WindowTokens(marks); held > room {
		return fmt.Errorf("the conversation holds about %d tokens and %s reads %d (num_ctx) less its %d-token reply reserve: compact it first, or choose a model with more context", held, m.Model, ep.NumCtx, reserve)
	}
	return nil
}
