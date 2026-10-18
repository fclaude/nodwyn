package rpc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/frontend/run"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

type ownedSession struct {
	session *core.Session
	driver  *run.Driver
	drained chan struct{}
	stream  *eventStream
}

// Lifecycle operations are serialized. This keeps construction, ownership and
// closing atomic across clients without teaching core about connections.
type server struct {
	options       core.Options
	schedulerLock *os.File
	scheduleSkips map[string]deferredSkip
	mu            sync.Mutex
	sessions      map[string]ownedSession
	watch         stateWatch
	// authorizations are the OAuth2 authorizations started from the
	// console (credentials.go).
	authorizations map[string]*authorization
	// resuming holds, for each task being resumed without the lock, a
	// channel closed when that resume is done.
	resuming map[string]chan struct{}
	// notify shows a notice on this computer's desktop (WithNotify): nil
	// in tests and in the browser demo, so neither ever shows one.
	// notifyOn is notify.desktop, read by the sessions' drain goroutines,
	// which do not hold mu.
	notify   func(title, body string) error
	notifyOn atomic.Bool
	// keysTold says a locked keychain stopping a scheduled run has been
	// told on the desktop since the keychain was last unlocked.
	keysTold bool
	// login is where this computer's login item goes and the nodwyn it
	// runs (WithStartup); a daemon without it offers none.
	login *loginItem
	// polled is when each workflow's source was last checked, pollErrors
	// what its last failed check said, and capped the day its daily spend
	// limit was last said to be reached: kept in memory, so a daemon that
	// starts checks every source at once.
	polled     map[string]time.Time
	pollErrors map[string]string
	capped     map[string]string
	// ctx ends when Serve is told to stop, and every wait on the network a
	// method makes without the lock ends with it (base). closing is set once
	// the sessions are being closed, which a session made after that does not
	// outlive (own).
	ctx     context.Context
	closing bool
}

// base is the context of work a method starts on its own: it ends at
// shutdown, so a slow provider or a long download does not keep the daemon
// from closing its sessions.
func (s *server) base() context.Context {
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// slow runs work without the lifecycle lock, as the model probes do: making
// a session can take a while (a worktree is checked out, with the repository's
// hooks and filters), winding one down waits for its turn, and a key
// derivation or a call to a provider takes seconds, so other methods go on
// meanwhile. Serve closes the sessions only after every method has returned,
// so one made meanwhile is not missed. A task named by id is marked as being
// resumed while work runs, so a second resume or scheduled run of it waits
// for this one instead of racing it; work is then to start, resume or close
// that task.
func (s *server) slow(id string, work func()) {
	var done chan struct{}
	if id != "" {
		done = make(chan struct{})
		if s.resuming == nil {
			s.resuming = make(map[string]chan struct{})
		}
		s.resuming[id] = done
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if id != "" {
			delete(s.resuming, id)
			close(done)
		}
	}()
	work()
}

type sessionInfo struct {
	ID           string    `json:"id"`
	Repo         string    `json:"repo"`
	Open         bool      `json:"open"`
	Busy         bool      `json:"busy"`
	FirstMessage string    `json:"first_message"`
	LastEvent    time.Time `json:"last_event"`
	InFlight     bool      `json:"in_flight"`
	Truncated    bool      `json:"truncated"`
	// Additive (ADR 30): what a task list shows. Changes are files the guard
	// allowed this session to write; Run is the live run when this daemon
	// drives one, else the last recorded run.state.
	Title   string     `json:"title"`
	Started time.Time  `json:"started"`
	Changes []string   `json:"changes"`
	Run     *run.State `json:"run,omitempty"`
	// Archived: a person put the session away (ADR 39).
	Archived bool `json:"archived"`
	// Worktree is the git worktree made for the session, whose path is
	// Repo; a list groups the task under its repository (ADR 34).
	Worktree *log.Worktree  `json:"worktree,omitempty"`
	Schedule *scheduleState `json:"schedule,omitempty"`
}
type budgetInfo struct {
	ToolCalls budget.Limit `json:"tool_calls"`
	Tokens    budget.Limit `json:"tokens"`
	Actions   budget.Limit `json:"actions"`
	Wall      budget.Limit `json:"wall_ns"`
	// Cost is the task's spend limit in millionths of a US dollar; a hard
	// limit of 0 is none.
	Cost budget.Limit `json:"cost"`
}
type sessionDetails struct {
	Bindings []binding.Binding `json:"bindings"`
	ID       string            `json:"id"`
	Repo     string            `json:"repo"`
	Busy     bool              `json:"busy"`
	Tools    []llm.Tool        `json:"tools"`
	Budgets  budgetInfo        `json:"budgets"`
	// Model is the model this conversation talks to now (ADR 40): the
	// configured or chosen one, never a key.
	Model      core.ModelState `json:"model"`
	Commands   []string        `json:"allowed_commands"`
	Writes     []string        `json:"allowed_writes"`
	TurnWrites bool            `json:"turn_writes"`
	TurnActor  string          `json:"turn_actor,omitempty"`
	// ApproveAll: "don't ask" is on, ordinary writes and commands approved
	// without a prompt (session.approve_all), turned on by ApproveActor.
	ApproveAll   bool   `json:"approve_all"`
	ApproveActor string `json:"approve_actor,omitempty"`
	// ApproveAllOffered: Settings offers the switch (approval.dont_ask),
	// so session.approve_all may turn it on; the console shows it only
	// then, or while it is on.
	ApproveAllOffered bool `json:"approve_all_offered"`
	// Workspace is the folders outside the root the session may use, as the
	// workspace list named them when it opened (ADR 22).
	Workspace config.Workspace `json:"workspace"`
	// Git says a git work tree is the undo at the root; otherwise nodwyn
	// keeps previous versions of files it replaces.
	Git bool `json:"git"`
	// MCP is what the session made of each configured server.
	MCP []core.MCPServer `json:"mcp"`
	// Worktree is the git worktree Repo is, made for this session (ADR 34).
	Worktree *log.Worktree `json:"worktree,omitempty"`
	// Judge is the judge the task asks now (ADR 47); its server is empty
	// when it has none.
	Judge core.JudgeState `json:"judge"`
	// Delegates are the models the task's model may hand work to (ADR 27,
	// amended 2026-10-09); none unless the task was opened with some.
	Delegates []string `json:"delegates"`
	// Limits says the task's turns stop at the turn limits; false is a task
	// a person let run as long as it needs (ADR 49), whose Budgets then
	// have a hard limit of 0, none, but for its spend limit.
	Limits   bool     `json:"limits"`
	RunSpend *float64 `json:"run_spend,omitempty"`
}

func (s *server) details(session *core.Session) sessionDetails {
	// The session's own limits: Settings may have changed the daemon's
	// since it opened, and a run's spend limit is its own.
	limits := session.Limits()
	trust, actor := session.TurnApproval()
	all, allActor := session.ApproveAll()
	limitsOn, _ := session.LimitsOn()
	var runSpend *float64
	if cost, ok := session.RunSpend(); ok {
		runSpend = &cost
	}
	return sessionDetails{RunSpend: runSpend, Limits: limitsOn, Bindings: session.Bindings(), ID: session.ID(), Repo: session.Repo(), Busy: session.Busy() || s.sessions[session.ID()].driver.Active(), Tools: session.Tools(), Budgets: budgetInfo{limits.ToolCalls, limits.Tokens, limits.Actions, limits.Wall, limits.Cost}, Model: session.Model(), Commands: session.Allowed(), Writes: session.AllowedWrites(), TurnWrites: trust, TurnActor: actor, ApproveAll: all, ApproveActor: allActor, ApproveAllOffered: s.options.Config.Approval.DontAsk, Workspace: session.Workspace(), Git: isGit(session.Repo()), MCP: orEmpty(session.MCP()), Worktree: session.Worktree(), Judge: session.Judge(), Delegates: orEmpty(session.Delegates())}
}

func orEmpty[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

func isGit(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}

// idle finds the open session id names for a method that changes it or
// reads what only a session at rest can answer, and refuses while a run
// drives it. doing completes "Stop the active run before ".
func (s *server) idle(id, doing string) (ownedSession, *rpcError) {
	if id == "" {
		return ownedSession{}, failure(-32602, "id is required")
	}
	owned, ok := s.sessions[id]
	if !ok {
		return ownedSession{}, failure(-32002, "Session is not open in this daemon")
	}
	if owned.driver.Active() {
		return ownedSession{}, failure(-32000, "Stop the active run before "+doing)
	}
	return owned, nil
}

// handler is one RPC method. Every handler runs with s.mu held and with
// the client already past hello, so none of them repeats those checks.
type handler func(*server, request, *peer) (any, *rpcError)

// methodTable is the dispatch. It is the one place a method name turns
// into behaviour; protocol.methods advertises the same set, and a test
// holds the two together.
var methodTable = map[string]handler{
	"run.start":                (*server).run,
	"run.info":                 (*server).run,
	"run.stop":                 (*server).run,
	"run.resume":               (*server).run,
	"session.policy.read":      (*server).policy,
	"session.policy.write":     (*server).policy,
	"session.answer":           (*server).answer,
	"session.secret":           (*server).answer,
	"session.approval.create":  (*server).answer,
	"session.binding.create":   (*server).answer,
	"session.approve_all":      (*server).approveAll,
	"session.watch":            (*server).subscribeState,
	"session.list":             (*server).list,
	"attention.list":           (*server).attentionList,
	"notes.read":               (*server).notesRead,
	"index.status":             (*server).indexStatus,
	"index.forget":             (*server).indexStatus,
	"session.notes.write":      (*server).notesWrite,
	"models.status":            (*server).modelsStatus,
	"models.offered":           (*server).modelsOffered,
	"models.published":         (*server).modelsPublished,
	"keychain.status":          (*server).keychain,
	"keychain.create":          (*server).keychain,
	"keychain.unlock":          (*server).keychain,
	"keychain.passphrase":      (*server).keychain,
	"credentials.list":         (*server).credentialsMethod,
	"credentials.add":          (*server).credentialsMethod,
	"credentials.replace":      (*server).credentialsMethod,
	"credentials.remove":       (*server).credentialsMethod,
	"credentials.bind":         (*server).credentialsMethod,
	"credentials.unbind":       (*server).credentialsMethod,
	"credentials.authorize":    (*server).credentialsMethod,
	"settings.read":            (*server).settingsMethod,
	"settings.control.write":   (*server).settingsMethod,
	"settings.provider.write":  (*server).providerWrite,
	"settings.provider.remove": (*server).providerRemove,
	"settings.model.write":     (*server).modelWrite,
	"settings.model.remove":    (*server).modelRemove,
	"models.test":              (*server).modelTest,
	"settings.docs.write":      (*server).docsSettingsMethod,
	"settings.write":           (*server).settingsWrite,
	"workspace.list":           (*server).workspaces,
	"workspace.forgotten":      (*server).workspaces,
	"workspace.active":         (*server).workspaces,
	"workspace.save":           (*server).workspaces,
	"workspace.remove":         (*server).workspaces,
	"versions.list":            (*server).versions,
	"versions.diff":            (*server).versions,
	"session.restore":          (*server).versions,
	"session.attach":           (*server).attach,
	"mcp.list":                 (*server).mcpMethod,
	"mcp.inspect":              (*server).mcpMethod,
	"mcp.save":                 (*server).mcpMethod,
	"mcp.approve":              (*server).mcpMethod,
	"mcp.remove":               (*server).mcpMethod,
	"mcp.runtime":              (*server).mcpMethod,
	"mcp.pull":                 (*server).mcpMethod,
	"session.open":             (*server).open,
	"worktree.open":            (*server).open,
	"worktree.diff":            (*server).worktreeDiff,
	"worktree.review":          (*server).worktreeReview,
	"session.resume":           (*server).resume,
	"session.model.set":        (*server).model,
	"session.judge.set":        (*server).judge,
	"session.limits.set":       (*server).limitsSet,
	"session.info":             (*server).info,
	"session.totals":           (*server).totals,
	"schedule.next":            (*server).nextSchedule,
	"schedule.read":            (*server).readSchedule,
	"schedule.set":             (*server).setSchedule,
	"schedule.pause":           (*server).pauseSchedule,
	"schedule.run":             (*server).runSchedule,
	"startup.read":             (*server).startupMethod,
	"startup.set":              (*server).startupMethod,
	"workflow.list":            (*server).workflowMethod,
	"workflow.read":            (*server).workflowMethod,
	"workflow.set":             (*server).workflowMethod,
	"workflow.remove":          (*server).workflowMethod,
	"workflow.pause":           (*server).workflowMethod,
	"workflow.check":           (*server).workflowMethod,
	"workflow.test":            (*server).workflowMethod,
	"session.close":            (*server).closeSession,
	"session.rename":           (*server).rename,
	"session.archive":          (*server).archive,
	"session.delete":           (*server).deleteSession,
	"session.abort":            (*server).abort,
	"session.unsubscribe":      (*server).unsubscribeSession,
	"session.friction":         (*server).friction,
	"session.eval":             (*server).eval,
	"session.send":             (*server).send,
	"session.subscribe":        (*server).subscribe,
	"log.read":                 (*server).readLog,
	"conversation.search":      (*server).searchConversation,
	"log.context":              (*server).readContext,
	"log.runs":                 (*server).readRuns,
}

func (s *server) dispatch(req request, client *peer) (any, *rpcError) {
	if req.Method == "hello" {
		var p helloParams
		if err := params(req.Params, &p, "protocol", "via", "passkey"); err != nil {
			return nil, err
		}
		if p.Protocol != ProtocolVersion {
			return nil, failure(-32001, "Unsupported protocol version")
		}
		if p.Passkey != "" && (p.Via == "" || !passkeyName.MatchString(p.Passkey)) {
			return nil, failure(-32602, "passkey names the passkey of a device given by via, such as laptop")
		}
		if p.Via != "" {
			if client.ready || !viaName.MatchString(p.Via) {
				return nil, failure(-32602, "via must be a short name such as web:phone, given once")
			}
			client.actor += " via " + p.Via
			// A device whose person proved themself with a passkey makes this
			// computer's choices (ADR 30, amended 2026-10-17). The claim needs
			// no proof here: a same-user client that claims nothing at all
			// already has every method, so it raises nobody above that.
			if p.Passkey != "" {
				client.actor += " passkey:" + p.Passkey
			} else {
				client.device = true
			}
		}
		client.ready = true
		return helloResult{ProtocolVersion, s.options.Version, methods, client.actor}, nil
	}
	if !client.ready {
		return nil, failure(-32001, "hello required before session methods")
	}
	call, ok := methodTable[req.Method]
	if !ok {
		return nil, failure(-32601, "Method not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return call(s, req, client)
}

// viaName is what a relaying client may claim: a short lower-case label.
var viaName = regexp.MustCompile(`^[a-z][a-z0-9]*:[a-z0-9][a-z0-9._-]{0,31}$`)

// passkeyName is a passkey's name, which is the name of the device that
// registered it.
var passkeyName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// store reads session logs from disk, for the methods that answer about
// sessions this daemon does not have open.
func (s *server) store() core.Store { return core.Store{Dir: s.options.Paths.SessionsDir} }

// session decodes an id parameter and finds the open session it names.
func (s *server) session(req request) (ownedSession, string, *rpcError) {
	var p idParams
	if err := params(req.Params, &p, "id"); err != nil {
		return ownedSession{}, "", err
	}
	if p.ID == "" {
		return ownedSession{}, "", failure(-32602, "id is required")
	}
	owned, open := s.sessions[p.ID]
	if !open {
		return ownedSession{}, p.ID, failure(-32002, "Session is not open in this daemon")
	}
	return owned, p.ID, nil
}

func (s *server) subscribeState(req request, client *peer) (any, *rpcError) {
	if err := params(req.Params, &struct{}{}); err != nil {
		return nil, err
	}
	return s.watch.subscribe(client), nil
}

func (s *server) list(req request, _ *peer) (any, *rpcError) {
	if err := params(req.Params, &struct{}{}); err != nil {
		return nil, err
	}
	// One log that cannot be read leaves the other tasks listed.
	infos, err := core.Readable(s.store().List())
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	result := make([]sessionInfo, 0, len(infos))
	for _, i := range infos {
		owned, open := s.sessions[i.ID]
		busy := open && (owned.session.Busy() || owned.driver.Active())
		entry := sessionInfo{ID: i.ID, Repo: i.Repo, Open: open, Busy: busy, FirstMessage: i.FirstMessage, LastEvent: i.LastEvent, InFlight: i.InFlight, Truncated: i.Truncated,
			Title: i.Title(), Started: i.Started, Changes: i.Changes, Archived: i.Archived, Worktree: i.Worktree}
		if entry.Changes == nil {
			entry.Changes = []string{}
		}
		if open && owned.driver != nil {
			state := owned.driver.Info()
			entry.Run = &state
		} else if i.Run != nil {
			entry.Run = &run.State{Task: i.Run.Task, Deadline: i.Run.Deadline, MaxTurns: i.Run.MaxTurns, Turns: i.Run.Turns, Status: i.Run.Status, Reason: i.Run.Reason, Unattended: i.Run.Unattended, AutoApprove: i.Run.AutoApprove, Judge: i.Run.Judge}
		}
		if state, err := s.scheduleState(i.ID); err == nil && state.Definition != nil {
			entry.Schedule = &state
		}
		result = append(result, entry)
	}
	return result, nil
}

// open starts a session in a folder: session.open in the folder itself,
// worktree.open in a new git worktree of it, on a branch of its own (ADR
// 34), which core makes and refuses for a folder that is not a whole
// repository with a commit. Both take the same model choice (ADR 40).
func (s *server) open(req request, client *peer) (any, *rpcError) {
	var p openParams
	if err := params(req.Params, &p, "repo", "name", "server", "model", "effort", "judge", "delegates", "limits"); err != nil {
		return nil, err
	}
	choice := core.ModelChoice{Name: p.Name, Server: p.Server, Model: p.Model, Effort: p.Effort, Actor: client.actor}
	if why := deviceModelRefusal(client, choice); why != nil {
		return nil, why
	}
	var judge *core.JudgeChoice
	if p.Judge != nil {
		if why := client.refuseDevice("The judge a task asks is chosen on the computer nodwyn runs on, not from a paired device"); why != nil {
			return nil, why
		}
		judge = &core.JudgeChoice{Server: *p.Judge, Actor: client.actor}
	}
	var delegates *core.DelegateChoice
	if len(p.Delegates) > 0 {
		// Where a task's work may go is this computer's choice, as its model
		// and judge are (ADR 30).
		if why := client.refuseDevice("The models a task may delegate to are chosen on the computer nodwyn runs on, not from a paired device"); why != nil {
			return nil, why
		}
		delegates = &core.DelegateChoice{Models: p.Delegates, Actor: client.actor}
	}
	var limits *core.LimitsChoice
	if p.Limits != nil && !*p.Limits {
		if why := deviceLimitsRefusal(client); why != nil {
			return nil, why
		}
		limits = &core.LimitsChoice{On: false, Actor: client.actor}
	}
	if err := s.folder("repo", p.Repo); err != nil {
		return nil, err
	}
	if why := s.lockedKeyRefusal(choice); why != "" {
		return nil, failure(-32000, why)
	}
	options := s.options
	options.Repo = p.Repo
	options.Model = choice
	options.Judge = judge
	options.Delegates = delegates
	options.Limits = limits
	options.Worktree = req.Method == "worktree.open"
	var session *core.Session
	var err error
	s.slow("", func() { session, err = core.New(options) })
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	s.own(session)
	return s.details(session), nil
}

// resume opens a session that exists on disk; one already open is simply
// described, so a client may call resume without checking first.
func (s *server) resume(req request, _ *peer) (any, *rpcError) {
	owned, id, err := s.session(req)
	// Another client is resuming it: wait for that, then describe what it
	// opened, as for a task already open.
	for err != nil && s.resuming[id] != nil {
		done := s.resuming[id]
		s.mu.Unlock()
		<-done
		s.mu.Lock()
		owned, id, err = s.session(req)
	}
	if err == nil {
		return s.details(owned.session), nil
	}
	if id == "" {
		return nil, err
	}
	if why := s.lockedKeyRefusal(core.ModelChoice{}); why != "" {
		return nil, failure(-32000, why)
	}
	options := s.options
	options.Repo = ""
	// As open does, without the lifecycle lock: a resume may make the
	// task's worktree again.
	var session *core.Session
	var resumeErr error
	s.slow(id, func() { session, resumeErr = core.Resume(options, id) })
	if resumeErr != nil {
		return nil, failure(-32000, resumeErr.Error())
	}
	s.own(session)
	return s.details(session), nil
}

// session.model.set moves a conversation to another model, from its next turn
// on (ADR 40). It names a [models.NAME] entry, config.toml's [control] model,
// or a model on a configured [servers.NAME]; the conversation's key does not
// change, and every request still checks its binding. It is this computer's
// act: another device steers an open task but does not decide where its
// content goes next (ADR 30).
func (s *server) model(req request, client *peer) (any, *rpcError) {
	var p modelSetParams
	if err := params(req.Params, &p, "id", "name", "server", "model", "effort"); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, failure(-32602, "id is required")
	}
	if p.Name == "" && p.Server == "" && p.Model == "" {
		return nil, failure(-32602, "give the name of a configured model, or a server and the model on it")
	}
	choice := core.ModelChoice{Name: p.Name, Server: p.Server, Model: p.Model, Effort: p.Effort, Actor: client.actor}
	if why := deviceModelRefusal(client, choice); why != nil {
		return nil, why
	}
	owned, rerr := s.idle(p.ID, "switching the model")
	if rerr != nil {
		return nil, rerr
	}
	state, err := owned.session.SetModel(choice)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return state, nil
}

// judge is session.judge.set: a task's judge, from its next question (ADR
// 47). The judge is shown the task's plan and commands, so where they go is
// this computer's choice, as a model's is (ADR 30), and a run, whose judge
// may be answering its confirms, keeps the one it started with.
func (s *server) judge(req request, client *peer) (any, *rpcError) {
	var p judgeSetParams
	if err := params(req.Params, &p, "id", "judge"); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, failure(-32602, "id is required")
	}
	if why := client.refuseDevice("The judge a task asks is chosen on the computer nodwyn runs on, not from a paired device"); why != nil {
		return nil, why
	}
	owned, rerr := s.idle(p.ID, "choosing the judge")
	if rerr != nil {
		return nil, rerr
	}
	state, err := owned.session.SetJudge(p.Judge, client.actor)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return state, nil
}

// limitsSet is session.limits.set: a task's turn limits on or off, from its
// next turn (ADR 49). The limits are what stops a turn nobody is watching,
// so lifting them is this computer's act, as Settings' limits are (ADR 30);
// a run keeps the limits it started with.
func (s *server) limitsSet(req request, client *peer) (any, *rpcError) {
	var p limitsSetParams
	if err := params(req.Params, &p, "id", "on"); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, failure(-32602, "id is required")
	}
	if why := deviceLimitsRefusal(client); why != nil {
		return nil, why
	}
	owned, rerr := s.idle(p.ID, "changing the task's limits")
	if rerr != nil {
		return nil, rerr
	}
	if err := owned.session.SetLimits(p.On, client.actor); err != nil {
		return nil, failure(-32000, err.Error())
	}
	on, _ := owned.session.LimitsOn()
	return limitsState{On: on}, nil
}

// deviceLimitsRefusal says why a paired device may not lift or change a
// task's turn limits: a device answers a task's requests, it does not
// decide how long a turn may run with nobody stopping it (ADR 30, ADR 49).
func deviceLimitsRefusal(client *peer) *rpcError {
	return client.refuseDevice("A task's limits are chosen on the computer nodwyn runs on, not from a paired device")
}

// deviceModelRefusal says why a paired device may not choose the model a
// conversation talks to, opening it or moving it: where its content goes is
// chosen on the computer nodwyn runs on (ADR 30, ADR 40). A device opens a
// task on config.toml's [control] model.
func deviceModelRefusal(client *peer, c core.ModelChoice) *rpcError {
	if c.Name == "" && c.Server == "" && c.Model == "" && c.Effort == "" {
		return nil
	}
	return client.refuseDevice("The model a task talks to is chosen on the computer nodwyn runs on, not from a paired device")
}

func (s *server) info(req request, _ *peer) (any, *rpcError) {
	owned, _, err := s.session(req)
	if err != nil {
		return nil, err
	}
	return s.details(owned.session), nil
}

// totals also reads closed tasks; it never resumes or executes a session.
func (s *server) totals(req request, _ *peer) (any, *rpcError) {
	var p idParams
	if err := params(req.Params, &p, "id"); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, failure(-32602, "id is required")
	}
	result, err := s.store().Totals(p.ID)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return result, nil
}

func (s *server) abort(req request, _ *peer) (any, *rpcError) {
	owned, _, err := s.session(req)
	if err != nil {
		return nil, err
	}
	owned.driver.Stop()
	owned.session.Abort()
	return struct{}{}, nil
}

func (s *server) unsubscribeSession(req request, client *peer) (any, *rpcError) {
	owned, _, err := s.session(req)
	if err != nil {
		return nil, err
	}
	owned.stream.mu.Lock()
	delete(owned.stream.subscribers, client)
	owned.stream.mu.Unlock()
	return struct{}{}, nil
}

func (s *server) closeSession(req request, _ *peer) (any, *rpcError) {
	owned, id, err := s.session(req)
	if err != nil {
		return nil, err
	}
	// The session leaves the list at once; winding it down waits for its turn,
	// which no other client should wait for.
	delete(s.sessions, id)
	var closeErr error
	s.slow(id, func() { closeErr = s.wind(owned) })
	if closeErr != nil {
		return nil, failure(-32000, closeErr.Error())
	}
	return struct{}{}, nil
}

// maxName bounds the name a person gives a session: a line in a list.
const maxName = 120

// rename names a session (ADR 39). An open session records the name in its
// own log, so a console showing it hears of it; a closed one is named
// through the store, whose lock refuses a session another process has open.
func (s *server) rename(req request, client *peer) (any, *rpcError) {
	var p renameParams
	if err := params(req.Params, &p, "id", "title"); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, failure(-32602, "id is required")
	}
	title := strings.Join(strings.Fields(p.Title), " ")
	if utf8.RuneCountInString(title) > maxName {
		return nil, failure(-32602, fmt.Sprintf("A name has at most %d characters", maxName))
	}
	var err error
	if owned, open := s.sessions[p.ID]; open {
		_, err = owned.session.Record(log.KindSessionTitle, log.SessionTitle{Actor: client.actor, Title: title})
	} else {
		err = s.store().Rename(p.ID, title, client.actor)
	}
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return struct{}{}, nil
}

// archive puts a closed session away or brings it back (ADR 39). An open
// one is refused: a task that can ask for an answer is never hidden.
func (s *server) archive(req request, client *peer) (any, *rpcError) {
	var p archiveParams
	if err := params(req.Params, &p, "id", "archived"); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, failure(-32602, "id is required")
	}
	if _, open := s.sessions[p.ID]; open {
		return nil, failure(-32000, "Close the task before archiving it")
	}
	if p.Archived {
		state, err := s.store().Schedule(p.ID)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		if state.Definition != nil {
			def := *state.Definition
			def.Paused, def.Actor, def.After = true, client.actor, state.Cursor
			if err := s.writeSchedule(p.ID, def); err != nil {
				return nil, failure(-32000, err.Error())
			}
		}
	}
	if err := s.store().Archive(p.ID, p.Archived, client.actor); err != nil {
		return nil, failure(-32000, err.Error())
	}
	return struct{}{}, nil
}

// deleteSession removes a closed session's log for good (ADR 39).
func (s *server) deleteSession(req request, _ *peer) (any, *rpcError) {
	var p idParams
	if err := params(req.Params, &p, "id"); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, failure(-32602, "id is required")
	}
	if _, open := s.sessions[p.ID]; open {
		return nil, failure(-32000, "Close the task before deleting it")
	}
	if err := s.store().Delete(p.ID); err != nil {
		return nil, failure(-32000, err.Error())
	}
	return struct{}{}, nil
}

func (s *server) friction(req request, client *peer) (any, *rpcError) {
	var p idParams
	if err := params(req.Params, &p, "id"); err != nil {
		return nil, err
	}
	owned, rerr := s.idle(p.ID, "inspecting friction")
	if rerr != nil {
		return nil, rerr
	}
	operation, readErr := owned.session.ReadFriction(client.actor)
	if readErr != nil {
		return nil, failure(-32000, readErr.Error())
	}
	return inspectionStarted{operation}, nil
}

func (s *server) eval(req request, client *peer) (any, *rpcError) {
	var p evalParams
	if err := params(req.Params, &p, "id", "source"); err != nil {
		return nil, err
	}
	if p.ID == "" || p.Source == "" {
		return nil, failure(-32602, "id and nonempty source are required")
	}
	owned, rerr := s.idle(p.ID, "evaluating")
	if rerr != nil {
		return nil, rerr
	}
	if err := owned.session.StartEval(p.Source, client.actor); err != nil {
		return nil, failure(-32000, err.Error())
	}
	return struct{}{}, nil
}

func (s *server) send(req request, _ *peer) (any, *rpcError) {
	var p sendParams
	if err := params(req.Params, &p, "id", "text"); err != nil {
		return nil, err
	}
	if p.ID == "" || p.Text == "" {
		return nil, failure(-32602, "id and nonempty text are required")
	}
	owned, rerr := s.idle(p.ID, "sending a message")
	if rerr != nil {
		return nil, rerr
	}
	if err := owned.session.Send(p.Text); err != nil {
		return nil, failure(-32000, err.Error())
	}
	return struct{}{}, nil
}

// readRange decodes the paging parameters shared by log.read and
// session.subscribe. through belongs only to log.read.
func readRange(req request, withThrough bool) (readParams, *rpcError) {
	var p readParams
	names := []string{"id", "after", "limit", "latest_run"}
	if withThrough {
		names = append(names, "through")
	}
	if err := params(req.Params, &p, names...); err != nil {
		return p, err
	}
	if p.ID == "" {
		return p, failure(-32602, "id is required")
	}
	if p.LatestRun && p.After != 0 {
		return p, failure(-32602, "latest_run cannot be combined with after")
	}
	if p.Limit == 0 {
		p.Limit = 100
	}
	if p.Limit < 1 || p.Limit > 1000 {
		return p, failure(-32602, "limit must be between 1 and 1000")
	}
	return p, nil
}

func (s *server) subscribe(req request, client *peer) (any, *rpcError) {
	p, err := readRange(req, false)
	if err != nil {
		return nil, err
	}
	owned, ok := s.sessions[p.ID]
	if !ok {
		return nil, failure(-32002, "Session is not open in this daemon")
	}
	result, subErr := owned.stream.subscribe(client, s.store(), p.After, p.Limit, p.LatestRun)
	if subErr != nil {
		return nil, failure(-32000, subErr.Error())
	}
	result.Busy = result.Busy || owned.driver.Active()
	return result, nil
}

func (s *server) readLog(req request, _ *peer) (any, *rpcError) {
	p, err := readRange(req, true)
	if err != nil {
		return nil, err
	}
	var page core.LogPage
	var readErr error
	if p.LatestRun {
		page, readErr = s.store().ReadLatestRun(p.ID, p.Through, p.Limit)
	} else {
		page, readErr = s.store().ReadLog(p.ID, p.After, p.Through, p.Limit)
	}
	if readErr != nil {
		return nil, failure(-32000, readErr.Error())
	}
	return page, nil
}

func (s *server) readRuns(req request, _ *peer) (any, *rpcError) {
	var p runsParams
	if err := params(req.Params, &p, "id", "before", "through", "limit"); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, failure(-32602, "id is required")
	}
	if p.Limit == 0 {
		p.Limit = 10
	}
	if p.Limit < 1 || p.Limit > 100 {
		return nil, failure(-32602, "limit must be between 1 and 100")
	}
	page, err := s.store().ReadRuns(p.ID, p.Before, p.Through, p.Limit)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return page, nil
}

func (s *server) own(session *core.Session, drivers ...*run.Driver) {
	if s.closing {
		// Made while the daemon was shutting down: nothing will close it
		// later.
		go func() {
			for range session.Events() {
			}
		}()
		_ = session.Close()
		return
	}
	var driver *run.Driver
	if len(drivers) > 0 {
		driver = drivers[0]
	}
	drained := make(chan struct{})
	stream := &eventStream{session: session, subscribers: make(map[*peer]uint64)}
	s.sessions[session.ID()] = ownedSession{session: session, driver: driver, drained: drained, stream: stream}
	s.watch.update(session, true, driver.Active())
	go func() {
		defer close(drained)
		for ev := range session.Events() {
			if driver != nil {
				driver.Observe(ev)
			}
			if driver.Active() && ev.Kind == event.Status {
				if p, ok := ev.Payload.(event.StatusInfo); ok && p.State == "idle" {
					ev.Payload = event.StatusInfo{State: "run", Detail: "Run active"}
				}
			}
			stream.publish(ev)
			s.watch.update(session, true, driver.Active())
		}
		stream.closed()
		s.watch.update(session, false)
	}()
}

// wind stops a session's run, closes it and waits for its events to drain.
// It waits for the turn, so it runs outside the lifecycle lock (slow).
func (s *server) wind(owned ownedSession) error {
	owned.driver.Stop()
	owned.driver.Wait()
	err := owned.session.Close()
	<-owned.drained
	return err
}

// close ends the authorizations and closes every session. Serve runs it
// beside the wait for the methods still running, since closing a session is
// what ends a wait inside one (an attachment's confirm).
func (s *server) close() error {
	s.mu.Lock()
	s.closing = true
	for _, a := range s.authorizations {
		a.cancel()
	}
	open := s.sessions
	s.sessions = make(map[string]ownedSession)
	s.mu.Unlock()
	var first error
	for _, owned := range open {
		if err := s.wind(owned); err != nil && first == nil {
			first = err
		}
	}
	return first
}
