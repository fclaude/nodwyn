package rpc

import (
	"os"

	"github.com/fclaude/nodwyn/internal/startup"
)

// The daemon's desktop notices (ADR 52 §11) have fixed words, as the app's
// notification does (docs/app.md): a lock screen shows them, so they never
// carry a task's title, its request or what it asks.
const (
	answerTitle = "nodwyn needs your answer"
	answerBody  = "Open nodwyn to review what it asks."
	keysTitle   = "nodwyn can't run a scheduled task"
	keysBody    = "Unlock your keys in nodwyn: the model needs one, and scheduled runs are skipped until you do."
)

// tell shows a notice on this computer's desktop when Settings has it on.
// It does not wait for the program that shows it.
func (s *server) tell(title, body string) {
	if s.notify == nil || !s.notifyOn.Load() {
		return
	}
	go func() { _ = s.notify(title, body) }()
}

// loginItem is where this computer's login item goes and the nodwyn it runs.
type loginItem struct {
	place startup.Place
	exe   string
}

// startupState is the login item as startup.read and startup.set answer.
// Offered is false for a daemon that has none to offer; Why says why it
// cannot be turned on.
type startupState struct {
	startup.Item
	Offered bool   `json:"offered"`
	Program string `json:"program,omitempty"`
	Why     string `json:"why,omitempty"`
}

type startupParams struct {
	On *bool `json:"on"`
}

func (s *server) startupState() startupState {
	if s.login == nil {
		return startupState{}
	}
	state := startupState{Item: s.login.place.Status(s.login.exe), Offered: true, Program: s.login.exe}
	switch {
	case !state.Supported:
		state.Why = "nodwyn has no login item on this system."
	case startup.Temporary(s.login.exe):
		state.Why = "This nodwyn is a temporary build (go run), gone when it exits: install one and turn this on from it."
	}
	return state
}

// startupMethod answers startup.read and startup.set. Both are this
// computer's: what starts at its login is its setup (ADR 30).
func (s *server) startupMethod(req request, client *peer) (any, *rpcError) {
	if why := client.refuseDevice("Starting at login is set on this computer, not from a paired device"); why != nil {
		return nil, why
	}
	if req.Method == "startup.read" {
		if err := params(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		return s.startupState(), nil
	}
	var p startupParams
	if err := params(req.Params, &p, "on"); err != nil {
		return nil, err
	}
	if p.On == nil {
		return nil, failure(-32602, "on is required")
	}
	state := s.startupState()
	if !state.Offered {
		return nil, failure(-32000, "This daemon offers no login item")
	}
	if *p.On {
		if state.Why != "" {
			return nil, failure(-32000, state.Why)
		}
		if err := s.login.place.Enable(s.login.exe, startup.Environment(os.LookupEnv)); err != nil {
			return nil, failure(-32000, err.Error())
		}
	} else if err := s.login.place.Disable(); err != nil {
		return nil, failure(-32000, err.Error())
	}
	return s.startupState(), nil
}
