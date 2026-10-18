package rpc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/schedule"
	"github.com/fclaude/nodwyn/internal/watch"
	"github.com/fclaude/nodwyn/internal/workflow"
)

// Workflows with a source (ADR 53, slice 3). The daemon that holds
// scheduler.lock polls each source when it is due, outside the lifecycle
// lock, then, holding it, fires what came: a run in the task of the event's
// subject, opened for it the first time, with the event attached as data and
// the workflow's message as the run's request.

// serving is the context of the daemon's life, which asking a forge or
// running a command is cut short by; Background for a server that was not
// started by Serve.
func (s *server) serving() context.Context {
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

func (s *server) flows() workflow.Store {
	return workflow.Store{Dir: filepath.Join(s.options.Paths.DataDir, "workflows")}
}

// poller asks sources with the keychain as it is now, and none while it is
// locked. A key goes only through the check a task's own requests pass: the
// binding table as it is now, the credential's scopes, the rule against plain
// HTTP to a remote host, and a refresh of an OAuth2 token about to expire.
func (s *server) poller() watch.Poller {
	if s.options.Keychain == nil {
		return watch.Poller{}
	}
	return watch.Poller{Authorize: s.keyed().Authorize}
}

// workflowLoop polls the workflows every 30 seconds, and at once on start.
func (s *server) workflowLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		s.pollWorkflows(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// pollWorkflows polls each workflow whose source is due. The polls ask
// forges and run commands, so they go without the lifecycle lock, which
// every request to the daemon takes.
func (s *server) pollWorkflows(ctx context.Context, now time.Time) {
	s.mu.Lock()
	if ctx.Err() != nil || s.ensureScheduler() != nil || s.schedulerLock == nil {
		s.mu.Unlock()
		return
	}
	states, _ := s.flows().List()
	poller := s.poller()
	var due []workflow.State
	for _, state := range states {
		d := state.Definition
		if d.Paused || now.Before(s.polled[state.ID].Add(time.Duration(d.Every)*time.Minute)) {
			continue
		}
		due = append(due, state)
	}
	s.mu.Unlock()
	for _, state := range due {
		def := state.Definition.Definition
		items, err := poller.PollLook(ctx, def.Source, watch.Look{Seen: state.Seen, IDsOnly: !state.Seeded && !def.Existing})
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		s.applyPoll(state.ID, items, err, now)
		s.mu.Unlock()
	}
}

// applyPoll records what a poll found and fires what passes. An event that
// cannot fire yet (its task busy, the workflow's runs all going, the keys
// locked, the day's spend reached) is not marked seen, so the next poll
// tries it again: events wait their turn. It runs under the lifecycle lock.
func (s *server) applyPoll(id string, items []watch.Item, pollErr error, now time.Time) {
	store := s.flows()
	state, err := store.Read(id)
	if err != nil || state.Definition == nil || state.Definition.Removed || state.Definition.Paused {
		return
	}
	if s.polled == nil {
		s.polled, s.pollErrors, s.capped = map[string]time.Time{}, map[string]string{}, map[string]string{}
	}
	s.polled[id] = now
	def := state.Definition.Definition
	if pollErr != nil {
		if errors.Is(pollErr, watch.ErrLocked) && !s.keysTold {
			s.keysTold = true
			s.tell(keysTitle, keysBody)
		}
		// A source that fails every poll says so once, until it says
		// something else or works again.
		if s.pollErrors[id] != pollErr.Error() {
			s.pollErrors[id] = pollErr.Error()
			_, _ = store.Append(id, workflow.KindSkip, workflow.Skip{Reason: "Could not check the source: " + pollErr.Error(), At: now})
		}
		return
	}
	delete(s.pollErrors, id)
	if !state.Seeded && !def.Existing {
		// What the source has when the workflow starts is recorded, not
		// handled: a workflow acts on what comes after.
		seen := map[string]string{}
		for _, item := range items {
			seen[item.ID] = item.Version
		}
		_, _ = store.Append(id, workflow.KindSeen, workflow.Seen{Items: seen})
		return
	}
	next := map[string]string{}
	for _, item := range items {
		if version, ok := state.Seen[item.ID]; ok {
			next[item.ID] = version
		}
	}
	var fire []watch.Event
	for _, ev := range watch.Changes(state.Seen, items, def.Source.On) {
		if ok, _ := watch.Passes(def.Filters, ev.Item); !ok {
			next[ev.Item.ID] = ev.Item.Version
			continue
		}
		fire = append(fire, ev)
	}
	if len(fire) > workflow.MaxEvents {
		_, _ = store.Append(id, workflow.KindSkip, workflow.Skip{Reason: fmt.Sprintf("%d events at once, more than %d: the workflow is paused; resume it to handle them", len(fire), workflow.MaxEvents), At: now})
		paused := *state.Definition
		paused.Paused, paused.Actor = true, "workflow"
		_, _ = store.Append(id, workflow.KindDefine, paused)
		return
	}
	today := now.In(time.Local).Format(schedule.DateLayout)
	for _, ev := range fire {
		if def.DayCost > 0 && s.workflowToday(state, now).Cost >= def.DayCost {
			if s.capped[id] != today {
				s.capped[id] = today
				_, _ = store.Append(id, workflow.KindSkip, workflow.Skip{Reason: fmt.Sprintf("the daily spend limit of $%g is reached; events wait until tomorrow", def.DayCost), At: now})
			}
			break
		}
		task, wait, err := s.fire(id, def, state, ev, now)
		if wait {
			continue
		}
		next[ev.Item.ID] = ev.Item.Version
		if err != nil {
			_, _ = store.Append(id, workflow.KindSkip, workflow.Skip{Item: ev.Item.ID, Reason: err.Error(), At: now})
			continue
		}
		subject := ""
		if def.Routing == workflow.PerSubject {
			subject = ev.Item.ID
		}
		if fired, err := store.Append(id, workflow.KindFire, workflow.Fire{Item: ev.Item.ID, Version: ev.Item.Version, Kind: ev.Kind, Subject: subject, Task: task, Summary: ev.Summary(def.Source), At: now}); err == nil {
			state = fired
		}
	}
	if !state.Seeded || !maps.Equal(next, state.Seen) {
		_, _ = store.Append(id, workflow.KindSeen, workflow.Seen{Items: next})
	}
}

// workflowToday is what the runs of a workflow's tasks started today came
// to.
func (s *server) workflowToday(state workflow.State, now time.Time) core.ScheduleDay {
	day := core.ScheduleDay{Day: now.In(time.Local).Format(schedule.DateLayout)}
	for _, task := range slices.Compact(slices.Sorted(maps.Values(state.Subjects))) {
		if info, err := s.store().Schedule(task); err == nil && info.Today.Day == day.Day {
			day.Runs += info.Today.Runs
			day.Cost += info.Today.Cost
		}
	}
	return day
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// fire starts a run for one event: in its subject's task, opened for it
// the first time, the event attached as data and the workflow's message
// sent as the request. wait says it cannot start yet and is to be tried at
// the next poll.
func (s *server) fire(id string, def workflow.Definition, state workflow.State, ev watch.Event, now time.Time) (task string, wait bool, err error) {
	running := 0
	for _, t := range state.Subjects {
		if o, ok := s.sessions[t]; ok && (o.session.Busy() || o.driver.Active()) {
			running++
		}
	}
	if running >= def.Runs() {
		return "", true, nil
	}
	subject := ""
	if def.Routing == workflow.PerSubject {
		subject = ev.Item.ID
	}
	task = state.Subjects[subject]
	if task != "" {
		// A task deleted or archived since is not where the subject's next
		// run goes: a new one is.
		if info, err := s.store().Info(task); err != nil || info.Archived {
			task = ""
		}
	}
	choice := core.ModelChoice{Name: def.Model, Actor: "workflow"}
	if _, open := s.sessions[task]; !open && s.lockedKeyRefusal(choice) != "" {
		return "", true, nil
	}
	options := s.options
	options.NoConfirm, options.AutoApprove, options.Unattended, options.JudgeApprove = false, false, false, false
	var session *core.Session
	if task == "" {
		options.Repo, options.Model, options.Worktree = def.Repo, choice, def.Worktree
		if def.NoLimits {
			options.Limits = &core.LimitsChoice{On: false, Actor: "workflow"}
		}
		var openErr error
		s.slow("", func() { session, openErr = core.New(options) })
		if openErr != nil {
			return "", false, openErr
		}
		s.own(session)
		title := def.Name
		switch {
		case subject != "" && def.Source.Kind == watch.Gmail:
			title = cmp.Or(ev.Item.Title, "(no subject)")
		case subject != "":
			title = fmt.Sprintf("#%s %s", ev.Item.ID, ev.Item.Title)
		}
		if _, err := session.Record(log.KindSessionOrigin, log.SessionOrigin{Workflow: id, Name: def.Name, Subject: subject}); err != nil {
			return session.ID(), false, err
		}
		if _, err := session.Record(log.KindSessionTitle, log.SessionTitle{Actor: "workflow", Title: title}); err != nil {
			return session.ID(), false, err
		}
	} else {
		if s.resuming[task] != nil {
			return "", true, nil
		}
		owned, open := s.sessions[task]
		if !open {
			options.Repo = ""
			var resumeErr error
			s.slow(task, func() { session, resumeErr = core.Resume(options, task) })
			if resumeErr != nil {
				return task, false, resumeErr
			}
			s.own(session)
			owned = s.sessions[task]
		}
		session = owned.session
		if session.Busy() || owned.driver.Active() {
			return "", true, nil
		}
		if pending, err := session.PendingPrompts(); err != nil || len(pending) > 0 {
			return "", true, nil
		}
	}
	task = session.ID()
	// The event goes in as a file attached, so the window frames it as data,
	// never instructions, and every client shows it as an attachment.
	dir, err := os.MkdirTemp("", "nodwyn-event-")
	if err != nil {
		return task, false, err
	}
	defer os.RemoveAll(dir)
	name := "event-" + unsafeName.ReplaceAllString(ev.Item.ID, "-") + ".md"
	switch def.Source.Kind {
	case watch.GiteaPulls, watch.GitHubPulls:
		name = "pull-request-" + unsafeName.ReplaceAllString(ev.Item.ID, "-") + ".md"
	case watch.Gmail:
		name = "email-" + unsafeName.ReplaceAllString(ev.Item.ID, "-") + ".md"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(ev.Text(def.Source)), 0o600); err != nil {
		return task, false, err
	}
	if _, err := session.Attach(path, name, "workflow "+def.Name); err != nil {
		return task, false, err
	}
	run := log.ScheduleRun{Due: now, Started: now, Actor: "workflow", Cost: def.Cost, Workflow: id, Event: ev.Summary(def.Source)}
	// Registered before the run starts, so that its end cannot be missed.
	watching := def.Then.Post != "" || def.Then.Notify == "run"
	if watching {
		pending := pendingRun{workflow: id, item: ev.Item.ID, subject: subject, summary: run.Event, url: ev.Item.URL, label: "#" + ev.Item.ID}
		if def.Source.Kind == watch.Gmail {
			pending.label = fmt.Sprintf("%q", cmp.Or(ev.Item.Title, "(no subject)"))
			if reply, err := watch.ReplyTo(ev.Item); err == nil {
				pending.reply = &reply
			}
		}
		s.runs.add(task, pending)
	}
	if err := session.SendScheduled(def.Message, run); err != nil {
		if watching {
			s.runs.take(task)
		}
		return task, false, err
	}
	return task, false, nil
}

// workflowView is a workflow as clients show it, in the daemon's words.
type workflowView struct {
	workflow.State
	Sentence string           `json:"sentence"`
	Status   string           `json:"status"`
	Checked  *time.Time       `json:"checked,omitempty"`
	Running  int              `json:"running"`
	Today    core.ScheduleDay `json:"today"`
	Recent   []fireView       `json:"recent"`
}

type fireView struct {
	workflow.Fire
	When string `json:"when"`
}

func (s *server) workflowView(state workflow.State) workflowView {
	now := time.Now()
	def := state.Definition.Definition
	view := workflowView{State: state, Sentence: def.Sentence(), Today: s.workflowToday(state, now), Recent: []fireView{}}
	if t, ok := s.polled[state.ID]; ok {
		view.Checked = &t
	}
	for _, t := range state.Subjects {
		if o, ok := s.sessions[t]; ok && (o.session.Busy() || o.driver.Active()) {
			view.Running++
		}
	}
	for i := len(state.Fires) - 1; i >= 0 && len(view.Recent) < 5; i-- {
		f := state.Fires[i]
		view.Recent = append(view.Recent, fireView{f, schedule.Label(f.At.In(time.Local), now)})
	}
	switch {
	case def.Paused:
		view.Status = "Paused"
	case s.pollErrors[state.ID] != "":
		view.Status = "Could not check the source: " + s.pollErrors[state.ID]
	case view.Checked == nil:
		view.Status = "Not checked yet"
	case !state.Seeded:
		view.Status = "Checked " + schedule.Label(view.Checked.In(time.Local), now)
	default:
		view.Status = fmt.Sprintf("Watching %d item%s, checked %s", len(state.Seen), map[bool]string{true: "", false: "s"}[len(state.Seen) == 1], schedule.Label(view.Checked.In(time.Local), now))
	}
	return view
}

type workflowSetParams struct {
	ID         string              `json:"id,omitempty"`
	Definition workflow.Definition `json:"definition"`
}

type workflowPauseParams struct {
	ID     string `json:"id"`
	Paused *bool  `json:"paused"`
}

type workflowTestParams struct {
	Definition workflow.Definition `json:"definition"`
}

// workflowTest is what Test the trigger shows: each item the source has
// now, whether it passes the filters (and why not), and what a new workflow
// does with it.
type workflowTest struct {
	Sentence string     `json:"sentence,omitempty"`
	Items    []testItem `json:"items"`
	Note     string     `json:"note"`
}

type testItem struct {
	watch.Item
	Passes bool   `json:"passes"`
	Why    string `json:"why,omitempty"`
}

func (s *server) workflowMethod(req request, client *peer) (any, *rpcError) {
	switch req.Method {
	case "workflow.list":
		if err := params(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		states, err := s.flows().List()
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		views := []workflowView{}
		for _, state := range states {
			views = append(views, s.workflowView(state))
		}
		return views, nil
	case "workflow.read":
		var p idParams
		if err := params(req.Params, &p, "id"); err != nil {
			return nil, err
		}
		state, err := s.flows().Read(p.ID)
		if err != nil || state.Definition == nil || state.Definition.Removed {
			return nil, failure(-32000, fmt.Sprintf("no workflow %q", p.ID))
		}
		return s.workflowView(state), nil
	case "workflow.set":
		if why := client.refuseDevice("Workflows are made and changed on this computer, not from a paired device"); why != nil {
			return nil, why
		}
		var p workflowSetParams
		if err := params(req.Params, &p, "id", "definition"); err != nil {
			return nil, err
		}
		if err := s.checkWorkflow(&p.Definition); err != nil {
			return nil, err
		}
		id := p.ID
		if id == "" {
			id = workflow.NewID()
		} else if state, err := s.flows().Read(id); err != nil || state.Definition == nil || state.Definition.Removed {
			return nil, failure(-32000, fmt.Sprintf("no workflow %q", id))
		}
		state, err := s.flows().Append(id, workflow.KindDefine, workflow.Define{Definition: p.Definition, Actor: client.actor})
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		// A new or changed source is checked at the next tick.
		delete(s.polled, id)
		return s.workflowView(state), nil
	case "workflow.remove":
		if why := client.refuseDevice("Workflows are removed on this computer, not from a paired device"); why != nil {
			return nil, why
		}
		state, rerr := s.workflowFor(req)
		if rerr != nil {
			return nil, rerr
		}
		removed := *state.Definition
		removed.Removed, removed.Actor = true, client.actor
		if _, err := s.flows().Append(state.ID, workflow.KindDefine, removed); err != nil {
			return nil, failure(-32000, err.Error())
		}
		return struct{}{}, nil
	case "workflow.pause":
		var p workflowPauseParams
		if err := params(req.Params, &p, "id", "paused"); err != nil {
			return nil, err
		}
		if p.Paused == nil {
			return nil, failure(-32602, "paused is required")
		}
		if !*p.Paused {
			if why := client.refuseDevice("Resuming a workflow is a change made on this computer, not from a paired device"); why != nil {
				return nil, why
			}
		}
		state, rerr := s.workflowFor(request{Params: mustID(p.ID)})
		if rerr != nil {
			return nil, rerr
		}
		changed := *state.Definition
		changed.Paused, changed.Actor = *p.Paused, client.actor
		state, err := s.flows().Append(state.ID, workflow.KindDefine, changed)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		delete(s.polled, state.ID)
		return s.workflowView(state), nil
	case "workflow.check":
		state, rerr := s.workflowFor(req)
		if rerr != nil {
			return nil, rerr
		}
		if err := s.ensureScheduler(); err != nil || s.schedulerLock == nil {
			return nil, failure(-32000, "The scheduler is owned by another daemon")
		}
		poller := s.poller()
		var items []watch.Item
		var pollErr error
		def := state.Definition.Definition
		look := watch.Look{Seen: state.Seen, IDsOnly: !state.Seeded && !def.Existing}
		s.slow("", func() { items, pollErr = poller.PollLook(s.serving(), def.Source, look) })
		s.applyPoll(state.ID, items, pollErr, time.Now())
		if state, err := s.flows().Read(state.ID); err == nil {
			return s.workflowView(state), nil
		}
		return nil, failure(-32000, "the workflow could not be read again")
	case "workflow.test":
		if why := client.refuseDevice("A workflow's source is tried on this computer, not from a paired device"); why != nil {
			return nil, why
		}
		var p workflowTestParams
		if err := params(req.Params, &p, "definition"); err != nil {
			return nil, err
		}
		if p.Definition.Source.Kind == watch.Command {
			if err := s.folder("repo", p.Definition.Repo); err != nil {
				return nil, err
			}
			p.Definition.Source.Dir = p.Definition.Repo
		}
		if err := p.Definition.Source.Validate(); err != nil {
			return nil, failure(-32602, err.Error())
		}
		for _, f := range p.Definition.Filters {
			if err := f.Validate(); err != nil {
				return nil, failure(-32602, err.Error())
			}
		}
		poller := s.poller()
		var items []watch.Item
		var pollErr error
		s.slow("", func() { items, pollErr = poller.Poll(s.serving(), p.Definition.Source) })
		if pollErr != nil {
			return nil, failure(-32000, "Could not check the source: "+pollErr.Error())
		}
		result := workflowTest{Items: []testItem{}, Note: "These are there now. A new workflow records them and acts on what comes after."}
		if p.Definition.Validate() == nil {
			result.Sentence = p.Definition.Sentence()
		}
		if p.Definition.Existing {
			result.Note = "These are there now. A new workflow acts on each that passes, at its first check."
		}
		for _, item := range items {
			ok, why := watch.Passes(p.Definition.Filters, item)
			result.Items = append(result.Items, testItem{item, ok, why})
		}
		return result, nil
	}
	return nil, failure(-32601, "Method not found")
}

func (s *server) workflowFor(req request) (workflow.State, *rpcError) {
	var p idParams
	if err := params(req.Params, &p, "id"); err != nil {
		return workflow.State{}, err
	}
	state, err := s.flows().Read(p.ID)
	if err != nil || state.Definition == nil || state.Definition.Removed {
		return workflow.State{}, failure(-32000, fmt.Sprintf("no workflow %q", p.ID))
	}
	return state, nil
}

// checkWorkflow refuses a definition that cannot run: one that does not
// validate, a folder a task may not work in, a model that is not configured.
// A command source runs in the workflow's folder, which it is given here.
func (s *server) checkWorkflow(d *workflow.Definition) *rpcError {
	if d.Source.Kind == watch.Command {
		d.Source.Dir = d.Repo
	}
	if err := d.Validate(); err != nil {
		return failure(-32602, err.Error())
	}
	if err := s.folder("repo", d.Repo); err != nil {
		return err
	}
	if _, _, err := core.Choose(s.options.Config, core.ModelChoice{Name: d.Model}); err != nil {
		return failure(-32602, err.Error())
	}
	return nil
}
