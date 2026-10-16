package core

import (
	"errors"
	"fmt"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// JudgeChoice names the judge a task asks (ADR 47): a [servers.NAME] whose
// provider answers typed questions, or "" for none, and who chose it, for
// the log.
type JudgeChoice struct {
	Server string
	Actor  string
}

// JudgeState is the judge a task asks now (ADR 47), as session.info reports
// it and a choice answers with. It names no key.
type JudgeState struct {
	// Server is the [servers.NAME] the judge is on; empty is none.
	Server   string `json:"server"`
	Provider string `json:"provider,omitempty"`
	Host     string `json:"host,omitempty"`
	Model    string `json:"model,omitempty"`
	// PriceInput and PriceOutput are what its questions cost, USD per
	// million tokens; absent when nobody says.
	PriceInput  *float64 `json:"price_input,omitempty"`
	PriceOutput *float64 `json:"price_output,omitempty"`
	// Available says the judge can be asked: its key is stored, and it is a
	// key its API takes. Why says why not, when there is a judge.
	Available bool   `json:"available"`
	Why       string `json:"why,omitempty"`
}

// initialJudge is the judge a session starts with: the one it was opened
// with, else config.toml's [judge] server while the judge is on.
func initialJudge(o Options) string {
	if o.Judge != nil {
		return o.Judge.Server
	}
	if _, ok := o.Config.JudgeServer(); ok {
		return o.Config.Judge.Server
	}
	return ""
}

// CheckJudge says why server cannot be a task's judge, or nil. Only a key
// stored now is known: one stored later is found when the judge is asked.
func CheckJudge(c config.Config, store *keychain.Store, server string) error {
	if server == "" {
		return nil
	}
	if c.Judge.Content == config.JudgeContentNone {
		return errors.New("the judge is off: judge.content is \"none\" in config.toml")
	}
	spec, err := c.JudgeOn(server)
	if err != nil {
		return err
	}
	if PlanSignIn(store, spec.Class) {
		return fmt.Errorf("%s signs in with a ChatGPT plan, which only reaches its chat models; the judge asks OpenAI's Decisions API with an API key, so add an OpenAI provider with one", server)
	}
	return nil
}

// PlanSignIn reports whether the key of class is a ChatGPT plan's sign-in,
// which the keychain sends only to the plan's model endpoints, never to the
// Decisions API (ADR 46, amended by ADR 47).
func PlanSignIn(store *keychain.Store, class string) bool {
	if store == nil || class == "" {
		return false
	}
	h := store.HandleForClass(class)
	if h == nil {
		return false
	}
	info, err := store.Info(h.ID)
	return err == nil && info.Provider == "openai" && info.Kind == keychain.KindOAuth2
}

// judgeSpec is the task's judge now, false when it has none.
func (s *Session) judgeSpec() (config.JudgeSpec, bool) {
	s.mu.Lock()
	server := s.judge
	s.mu.Unlock()
	if server == "" || s.opts.Config.Judge.Content == config.JudgeContentNone {
		return config.JudgeSpec{}, false
	}
	spec, err := s.opts.Config.JudgeOn(server)
	return spec, err == nil
}

// judgeHandle is the stored key of spec's server, or why the judge cannot
// send it.
func (s *Session) judgeHandle(spec config.JudgeSpec) (*scheme.Handle, string) {
	switch {
	case s.keychain == nil:
		return nil, "the keychain is not open"
	case PlanSignIn(s.keychain, spec.Class):
		return nil, fmt.Sprintf("%s signs in with a ChatGPT plan, which the Decisions API does not take", spec.Server)
	}
	h := s.HandleForClass(spec.Class)
	if h == nil {
		return nil, fmt.Sprintf("no key is stored for %s", spec.Server)
	}
	return h, ""
}

// JudgeOptions are the task's judge now, as the judge's primitives ask it
// on every call (prim.JudgeClient); false when it has none.
func (s *Session) JudgeOptions() (prim.JudgeOptions, bool) {
	spec, ok := s.judgeSpec()
	if !ok {
		return prim.JudgeOptions{}, false
	}
	j := s.opts.Config.Judge
	timeout, err := time.ParseDuration(j.Timeout)
	if err != nil {
		timeout = 10 * time.Second
	}
	return prim.JudgeOptions{Provider: spec.Provider, URL: spec.URL, Model: spec.Model, Class: spec.Class, Content: judgeContent(s.opts.Config), Timeout: timeout, PriceInput: spec.PriceInput, PriceOutput: spec.PriceOutput}, true
}

// judgeContent is what the judge may be shown (ADR 19), "plan" when unset.
func judgeContent(c config.Config) string {
	if c.Judge.Content == "" {
		return config.JudgeContentPlan
	}
	return c.Judge.Content
}

// Judge reports the judge the task asks now.
func (s *Session) Judge() JudgeState {
	spec, ok := s.judgeSpec()
	if !ok {
		return JudgeState{}
	}
	state := JudgeState{Server: spec.Server, Provider: spec.Provider, Host: prim.JudgeHost(prim.JudgeOptions{URL: spec.URL}), Model: spec.Model, PriceInput: spec.PriceInput, PriceOutput: spec.PriceOutput}
	_, state.Why = s.judgeHandle(spec)
	state.Available = state.Why == ""
	return state
}

// SetJudge makes server the task's judge, or none with "", from its next
// question (ADR 47). It is the person's choice of where the task's plan and
// commands go to be judged, so it is recorded with who made it, and a
// resumed task keeps it. It waits for no turn: a judge chosen while the
// task is answering would judge half of a turn, so it is refused then.
func (s *Session) SetJudge(server, actor string) (JudgeState, error) {
	if err := CheckJudge(s.opts.Config, s.keychain, server); err != nil {
		return JudgeState{}, err
	}
	if _, ok := s.interp.Lookup("judge-yes-no"); server != "" && !ok {
		return JudgeState{}, errors.New("this task opened with no judge it could ask (no judge provider configured, or no keychain open), so it cannot ask one now: close it and resume it")
	}
	done, err := s.holdIdle()
	if err != nil {
		return JudgeState{}, err
	}
	defer done()
	// The record goes in before the judge changes, so the questions asked
	// of the new judge come after it in the log.
	if _, err := s.Record(log.KindSessionJudge, judgeRecord(s.opts.Config, server, actor)); err != nil {
		return JudgeState{}, err
	}
	s.mu.Lock()
	s.judge = server
	s.mu.Unlock()
	if server != "" {
		o, _ := s.JudgeOptions()
		prim.RetargetJudge(s.interp, o)
	}
	return s.Judge(), nil
}

// judgeRecord is the session.judge record of a task whose judge is on
// server, chosen by actor.
func judgeRecord(c config.Config, server, actor string) log.SessionJudge {
	rec := log.SessionJudge{Actor: actor, Server: server}
	if spec, err := c.JudgeOn(server); err == nil {
		rec.Provider, rec.Host, rec.Model = spec.Provider, prim.JudgeHost(prim.JudgeOptions{URL: spec.URL}), spec.Model
	}
	return rec
}

// holdIdle holds an idle session for a change between turns: no turn or
// attachment starts until done is called, and Close waits for it.
func (s *Session) holdIdle() (done func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.busyWith() {
	case holdClosed:
		return nil, errors.New("core: session is closed")
	case holdTurn:
		return nil, errors.New("the task is answering: choose its judge when this turn ends")
	case holdAttachment:
		return nil, errors.New("an attachment is being added: choose the judge when it is done")
	case holdSwitch:
		return nil, errors.New("another change is in progress; wait until it is done")
	}
	return s.holdSwitching(), nil
}

// lastJudgeChoice is the judge the log last chose for this task, if it
// chose one: the person's choice outlives the process, as the task does.
func lastJudgeChoice(events []log.Event) (rec log.SessionJudge, found bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != log.KindSessionJudge {
			continue
		}
		if err := events[i].Decode(&rec); err != nil {
			return log.SessionJudge{}, false
		}
		return rec, true
	}
	return log.SessionJudge{}, false
}
