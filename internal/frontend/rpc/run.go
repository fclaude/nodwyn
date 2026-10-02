package rpc

import (
	"errors"
	"os"
	"path/filepath"
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
}

// resumeParams resume a run. How it goes on is said again, never read back
// from the log: a resume grants nothing the person did not ask for now.
type resumeParams struct {
	ID          string `json:"id"`
	AutoApprove bool   `json:"auto_approve,omitempty"`
	Unattended  bool   `json:"unattended,omitempty"`
}
type runResult struct {
	ID    string    `json:"id"`
	State run.State `json:"state"`
}

func (s *server) run(req request, client *peer) (any, *rpcError) {
	if req.Method == "run.start" {
		var p runParams
		if err := params(req.Params, &p, "repo", "task", "wall", "max_turns", "auto_approve", "unattended"); err != nil {
			return nil, err
		}
		info, err := os.Stat(p.Repo)
		if !filepath.IsAbs(p.Repo) || err != nil || !info.IsDir() {
			return nil, failure(-32602, "repo must be an existing absolute directory")
		}
		if why := s.rootRefusal(p.Repo); why != "" {
			return nil, failure(-32602, why)
		}
		if why := s.lockedKeyRefusal(); why != "" {
			return nil, failure(-32000, why)
		}
		wall, err := time.ParseDuration(p.Wall)
		if err != nil || wall <= 0 {
			return nil, failure(-32602, "wall must be a positive duration")
		}
		state := run.State{Task: p.Task, Deadline: time.Now().Add(wall), MaxTurns: p.MaxTurns, Unattended: p.Unattended, AutoApprove: p.AutoApprove}
		if err := state.Validate(); err != nil {
			return nil, failure(-32602, err.Error())
		}
		options := s.options
		options.Repo = p.Repo
		options.NoConfirm = false
		// Without this a run stops at the first write and waits for a client
		// that may not exist, until its budget kills it.
		options.AutoApprove = p.AutoApprove
		options.Unattended = p.Unattended
		session, err := core.New(options)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		return s.startRun(session, state, client.actor)
	}
	var p resumeParams
	allowed := []string{"id"}
	if req.Method == "run.resume" {
		allowed = append(allowed, "auto_approve", "unattended")
	}
	if err := params(req.Params, &p, allowed...); err != nil {
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
	state.Unattended, state.AutoApprove = p.Unattended, p.AutoApprove
	if open {
		owned.driver.Stop()
		owned.driver.Wait()
		if err := owned.session.Close(); err != nil {
			return nil, failure(-32000, err.Error())
		}
		<-owned.drained
		delete(s.sessions, p.ID)
	}
	options := s.options
	options.Repo = ""
	options.NoConfirm = false
	options.AutoApprove, options.Unattended = p.AutoApprove, p.Unattended
	session, err := core.Resume(options, p.ID)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return s.startRun(session, state, client.actor)
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
	driver.Start(actor)
	return runResult{session.ID(), driver.Info()}, nil
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
