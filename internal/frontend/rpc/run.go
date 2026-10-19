package rpc

import (
	"errors"
	"time"

	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/run"
	"github.com/fclaude/nodwyn/internal/log"
)

type runParams struct {
	Repo     string `json:"repo"`
	Task     string `json:"task"`
	Wall     string `json:"wall"`
	MaxTurns int    `json:"max_turns"`
	// AutoApprove answers this run's ordinary confirms with yes. It is a
	// property of the run, never of the daemon: a daemon that auto-approved
	// everything would do so for sessions opened later from a browser too.
	AutoApprove bool `json:"auto_approve"`
	// Unattended: nobody answers the run's prompts, and it goes on from a
	// turn only when that turn marked a plan step done (ADR 18 §5).
	Unattended bool `json:"unattended,omitempty"`
	// Judge has the judge answer the run's ordinary tool confirms
	// instead of a person; it excludes auto_approve and needs a judge.
	Judge bool `json:"judge,omitempty"`
	// Cost is the run's spend limit in US dollars, for the whole run; it
	// replaces budget.cost_hard. Omitted, the configured one applies.
	Cost float64 `json:"cost,omitempty"`
	// Sandbox "container" declares the machine the run's disposable
	// sandbox (ADR 54): the guard lets its tools reach any path but
	// nodwyn's own directories and credential files, what its commands
	// start in the background keeps running, and it is unattended and
	// auto-approved. Omitted, the run is pinned to its folder as any other.
	Sandbox string `json:"sandbox,omitempty"`
}

// resumeParams resume a run. How it goes on is said again, never read back
// from the log: a resume grants nothing the person did not ask for now.
type resumeParams struct {
	ID          string `json:"id"`
	AutoApprove bool   `json:"auto_approve,omitempty"`
	Unattended  bool   `json:"unattended,omitempty"`
	Judge       bool   `json:"judge,omitempty"`
	Sandbox     string `json:"sandbox,omitempty"`
}
type runResult struct {
	ID    string    `json:"id"`
	State run.State `json:"state"`
}

func (s *server) run(req request, client *peer) (any, *rpcError) {
	if req.Method == "run.start" {
		var p runParams
		if err := params(req.Params, &p, "repo", "task", "wall", "max_turns", "auto_approve", "unattended", "judge", "cost", "sandbox"); err != nil {
			return nil, err
		}
		sandbox, bad := sandboxMode(p.Sandbox, p.Judge)
		if bad != nil {
			return nil, bad
		}
		if sandbox {
			p.AutoApprove, p.Unattended = true, true
		}
		if p.Judge && p.AutoApprove {
			return nil, failure(-32602, "judge and auto_approve exclude each other: the judge answers what auto_approve would")
		}
		if err := unwatchedRefusal(client, p.AutoApprove, p.Unattended, p.Judge); err != nil {
			return nil, err
		}
		check := s.folder
		if sandbox {
			check = s.sandboxFolder
		}
		if err := check("repo", p.Repo); err != nil {
			return nil, err
		}
		if why := s.lockedKeyRefusal(core.ModelChoice{}); why != "" {
			return nil, failure(-32000, why)
		}
		wall, err := time.ParseDuration(p.Wall)
		if err != nil || wall <= 0 {
			return nil, failure(-32602, "wall must be a positive duration")
		}
		state := run.State{Task: p.Task, Deadline: time.Now().Add(wall), MaxTurns: p.MaxTurns, Unattended: p.Unattended, AutoApprove: p.AutoApprove, Judge: p.Judge, Cost: p.Cost, Sandbox: sandbox}
		if err := state.Validate(); err != nil {
			return nil, failure(-32602, err.Error())
		}
		options := s.options
		options.Config.Budget = options.Config.Budget.WithCostHard(p.Cost)
		if _, err := options.Config.Budget.Limits(options.Config.Loop.MaxToolCalls); err != nil {
			return nil, failure(-32602, err.Error())
		}
		options.Repo = p.Repo
		options.NoConfirm = false
		// Without this a run stops at the first write and waits for a client
		// that may not exist, until its budget kills it.
		options.AutoApprove = p.AutoApprove
		options.Unattended = p.Unattended
		options.JudgeApprove = p.Judge
		options.Sandbox = sandbox
		var session *core.Session
		s.slow("", func() { session, err = core.New(options) })
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		return s.startRun(session, state, client.actor)
	}
	var p resumeParams
	allowed := []string{"id"}
	if req.Method == "run.resume" {
		allowed = append(allowed, "auto_approve", "unattended", "judge", "sandbox")
	}
	if err := params(req.Params, &p, allowed...); err != nil {
		return nil, err
	}
	sandbox, bad := sandboxMode(p.Sandbox, p.Judge)
	if bad != nil {
		return nil, bad
	}
	if sandbox {
		p.AutoApprove, p.Unattended = true, true
	}
	if p.Judge && p.AutoApprove {
		return nil, failure(-32602, "judge and auto_approve exclude each other: the judge answers what auto_approve would")
	}
	if err := unwatchedRefusal(client, p.AutoApprove, p.Unattended, p.Judge); err != nil {
		return nil, err
	}
	owned, open := s.sessions[p.ID]
	if req.Method == "run.stop" {
		if !open || owned.driver == nil {
			return nil, failure(-32002, "Run is not open in this daemon")
		}
		owned.driver.Stop()
		return struct{}{}, nil
	}
	if req.Method == "run.info" && open && owned.driver != nil {
		return owned.driver.Info(), nil
	}
	if req.Method == "run.resume" && open && (owned.session.Busy() || owned.driver.Active()) {
		return nil, failure(-32000, "Stop the running session before resuming")
	}
	var history []log.Event
	if open {
		history = owned.session.EventsSoFar()
	} else {
		var err error
		history, err = readRunHistory(s.options.Paths.SessionsDir, p.ID)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
	}
	state, err := run.Restore(history)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	if req.Method == "run.info" {
		if state.Status == "running" {
			state.Status = "paused"
		}
		return state, nil
	}
	if state.Status == "completed" {
		return nil, failure(-32000, "Run already reported completion")
	}
	if err := state.Validate(); err != nil {
		return nil, failure(-32000, err.Error())
	}
	state.Unattended, state.AutoApprove, state.Judge, state.Sandbox = p.Unattended, p.AutoApprove, p.Judge, sandbox
	if open {
		delete(s.sessions, p.ID)
	}
	options := s.options
	options.Config.Budget = options.Config.Budget.WithCostHard(state.Cost)
	options.Repo = ""
	options.NoConfirm = false
	options.AutoApprove, options.Unattended, options.JudgeApprove, options.Sandbox = p.AutoApprove, p.Unattended, p.Judge, sandbox
	var session *core.Session
	var closeErr error
	s.slow(p.ID, func() {
		if open {
			closeErr = s.wind(owned)
		}
		if closeErr == nil {
			session, err = core.Resume(options, p.ID)
		}
	})
	if closeErr != nil {
		return nil, failure(-32000, closeErr.Error())
	}
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return s.startRun(session, state, client.actor)
}

// sandboxMode reads a run's sandbox parameter: "container" is sandbox mode
// (ADR 54), "" none, and anything else, or sandbox mode with the judge,
// which it excludes as auto_approve does, is refused.
func sandboxMode(value string, judge bool) (bool, *rpcError) {
	switch value {
	case "":
		return false, nil
	case "container":
		if judge {
			return false, failure(-32602, "sandbox and judge exclude each other: a sandbox run answers its ordinary confirms itself, as auto_approve does")
		}
		return true, nil
	}
	return false, failure(-32602, `sandbox must be "container" (the machine is a disposable container) or omitted`)
}

// unwatchedRefusal says why a paired device may not start or resume a run
// whose requests a person does not answer (auto_approve, judge or
// unattended). Like "Always allow" for uploads, that is chosen on the
// computer nodwyn runs on (ADR 30): a device answers requests, it does not
// hand them to no one. The bridge relays a paired device's name as via, so
// its peer is a device; a process claiming it only limits itself.
func unwatchedRefusal(client *peer, autoApprove, unattended, judge bool) *rpcError {
	if !(autoApprove || unattended || judge) {
		return nil
	}
	return client.refuseDevice("A run whose requests nobody answers by hand starts only on the computer nodwyn runs on, not from a paired device")
}

func (s *server) startRun(session *core.Session, state run.State, actor string) (any, *rpcError) {
	driver, err := run.New(session, state)
	if err != nil { // Drain before closing even if the deadline expired during setup.
		go func() {
			for range session.Events() {
			}
		}()
		_ = session.Close()
		return nil, failure(-32000, err.Error())
	}
	s.own(session, driver)
	// The reply is the run as started, not whatever execute has already done
	// by the time this goroutine is scheduled: it may already have begun the
	// next turn, and progress is watched with run.info and subscriptions.
	started := driver.Info()
	driver.Start(actor)
	return runResult{session.ID(), started}, nil
}
func readRunHistory(dir, id string) ([]log.Event, error) {
	store := core.Store{Dir: dir}
	var result []log.Event
	var after, through uint64
	for {
		page, err := store.ReadLog(id, after, through, 1000)
		if err != nil {
			return nil, err
		}
		if page.Truncated {
			return nil, errors.New("run log has a truncated tail; review and recover it before resuming")
		}
		if through == 0 {
			through = page.Through
		}
		result = append(result, page.Events...)
		if !page.More {
			return result, nil
		}
		if len(page.Events) == 0 {
			return nil, errors.New("empty run log page")
		}
		after = page.Events[len(page.Events)-1].Seq
	}
}
