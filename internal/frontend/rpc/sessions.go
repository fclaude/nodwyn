package rpc

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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
	options  core.Options
	mu       sync.Mutex
	sessions map[string]ownedSession
	watch    stateWatch
	// authorizations are the OAuth2 authorizations started from the
	// console (credentials.go).
	authorizations map[string]*authorization
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
	Bindings   []binding.Binding `json:"bindings"`
	ID         string            `json:"id"`
	Repo       string            `json:"repo"`
	Busy       bool              `json:"busy"`
	Tools      []llm.Tool        `json:"tools"`
	Budgets    budgetInfo        `json:"budgets"`
	Commands   []string          `json:"allowed_commands"`
	Writes     []string          `json:"allowed_writes"`
	TurnWrites bool              `json:"turn_writes"`
	TurnActor  string            `json:"turn_actor,omitempty"`
	// ApproveAll: "don't ask" is on, ordinary writes and commands approved
	// without a prompt (session.approve_all), turned on by ApproveActor.
	ApproveAll   bool   `json:"approve_all"`
	ApproveActor string `json:"approve_actor,omitempty"`
	// Workspace is what the session loaded from .nodwyn/workspace.toml.
	Workspace config.Workspace `json:"workspace"`
	// Git says a git work tree is the undo at the root; otherwise nodwyn
	// keeps previous versions of files it replaces.
	Git bool `json:"git"`
	// MCP is what the session made of each configured server.
	MCP []core.MCPServer `json:"mcp"`
}

func (s *server) details(session *core.Session) sessionDetails {
	// The session's own limits: Settings may have changed the daemon's
	// since it opened, and a run's spend limit is its own.
	limits := session.Limits()
	trust, actor := session.TurnApproval()
	all, allActor := session.ApproveAll()
	return sessionDetails{Bindings: session.Bindings(), ID: session.ID(), Repo: session.Repo(), Busy: session.Busy() || s.sessions[session.ID()].driver.Active(), Tools: session.Tools(), Budgets: budgetInfo{limits.ToolCalls, limits.Tokens, limits.Actions, limits.Wall, limits.Cost}, Commands: session.Allowed(), Writes: session.AllowedWrites(), TurnWrites: trust, TurnActor: actor, ApproveAll: all, ApproveActor: allActor, Workspace: session.Workspace(), Git: isGit(session.Repo()), MCP: orEmpty(session.MCP())}
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

// handler is one RPC method. Every handler runs with s.mu held and with
// the client already past hello, so none of them repeats those checks.
type handler func(*server, request, *peer) (any, *rpcError)

// methodTable is the dispatch. It is the one place a method name turns
// into behaviour; protocol.methods advertises the same set, and a test
// holds the two together.
var methodTable = map[string]handler{
	"run.start":               (*server).run,
	"run.info":                (*server).run,
	"run.stop":                (*server).run,
	"run.resume":              (*server).run,
	"session.policy.read":     (*server).policy,
	"session.policy.write":    (*server).policy,
	"session.answer":          (*server).answer,
	"session.secret":          (*server).answer,
	"session.approval.create": (*server).answer,
	"session.binding.create":  (*server).answer,
	"session.approve_all":     (*server).approveAll,
	"session.watch":           (*server).subscribeState,
	"session.list":            (*server).list,
	"attention.list":          (*server).attentionList,
	"notes.read":              (*server).notesRead,
	"session.notes.write":     (*server).notesWrite,
	"models.status":           (*server).modelsStatus,
	"models.offered":          (*server).modelsOffered,
	"keychain.status":         (*server).keychain,
	"keychain.create":         (*server).keychain,
	"keychain.unlock":         (*server).keychain,
	"keychain.passphrase":     (*server).keychain,
	"credentials.list":        (*server).credentialsMethod,
	"credentials.add":         (*server).credentialsMethod,
	"credentials.replace":     (*server).credentialsMethod,
	"credentials.remove":      (*server).credentialsMethod,
	"credentials.bind":        (*server).credentialsMethod,
	"credentials.authorize":   (*server).credentialsMethod,
	"settings.read":           (*server).settingsMethod,
	"settings.control.write":  (*server).settingsMethod,
	"settings.docs.write":     (*server).docsSettingsMethod,
	"settings.write":          (*server).settingsWrite,
	"workspace.list":          (*server).workspaces,
	"workspace.save":          (*server).workspaces,
	"workspace.remove":        (*server).workspaces,
	"versions.list":           (*server).versions,
	"session.restore":         (*server).versions,
	"session.attach":          (*server).attach,
	"mcp.list":                (*server).mcpMethod,
	"mcp.inspect":             (*server).mcpMethod,
	"mcp.save":                (*server).mcpMethod,
	"mcp.approve":             (*server).mcpMethod,
	"mcp.remove":              (*server).mcpMethod,
	"mcp.runtime":             (*server).mcpMethod,
	"mcp.pull":                (*server).mcpMethod,
	"session.open":            (*server).open,
	"session.resume":          (*server).resume,
	"session.info":            (*server).info,
	"session.close":           (*server).closeSession,
	"session.rename":          (*server).rename,
	"session.archive":         (*server).archive,
	"session.delete":          (*server).deleteSession,
	"session.abort":           (*server).abort,
	"session.unsubscribe":     (*server).unsubscribeSession,
	"session.friction":        (*server).friction,
	"session.eval":            (*server).eval,
	"session.send":            (*server).send,
	"session.subscribe":       (*server).subscribe,
	"log.read":                (*server).readLog,
}

func (s *server) dispatch(req request, client *peer) (any, *rpcError) {
	if req.Method == "hello" {
		var p helloParams
		if err := params(req.Params, &p, "protocol", "via"); err != nil {
			return nil, err
		}
		if p.Protocol != ProtocolVersion {
			return nil, failure(-32001, "Unsupported protocol version")
		}
		if p.Via != "" {
			if client.ready || !viaName.MatchString(p.Via) {
				return nil, failure(-32602, "via must be a short name such as web:phone, given once")
			}
			client.actor += " via " + p.Via
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
	infos, err := s.store().List()
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	result := make([]sessionInfo, 0, len(infos))
	for _, i := range infos {
		owned, open := s.sessions[i.ID]
		busy := open && (owned.session.Busy() || owned.driver.Active())
		entry := sessionInfo{ID: i.ID, Repo: i.Repo, Open: open, Busy: busy, FirstMessage: i.FirstMessage, LastEvent: i.LastEvent, InFlight: i.InFlight, Truncated: i.Truncated,
			Title: i.Title(), Started: i.Started, Changes: i.Changes, Archived: i.Archived}
		if entry.Changes == nil {
			entry.Changes = []string{}
		}
		if open && owned.driver != nil {
			state := owned.driver.Info()
			entry.Run = &state
		} else if i.Run != nil {
			entry.Run = &run.State{Task: i.Run.Task, Deadline: i.Run.Deadline, MaxTurns: i.Run.MaxTurns, Turns: i.Run.Turns, Status: i.Run.Status, Reason: i.Run.Reason, Unattended: i.Run.Unattended, AutoApprove: i.Run.AutoApprove, Judge: i.Run.Judge}
		}
		result = append(result, entry)
	}
	return result, nil
}

func (s *server) open(req request, _ *peer) (any, *rpcError) {
	var p openParams
	if err := params(req.Params, &p, "repo"); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(p.Repo) {
		return nil, failure(-32602, "repo must be an absolute directory")
	}
	if info, err := os.Stat(p.Repo); err != nil || !info.IsDir() {
		return nil, failure(-32602, "repo must be an existing directory")
	}
	if why := s.rootRefusal(p.Repo); why != "" {
		return nil, failure(-32602, why)
	}
	if why := s.lockedKeyRefusal(); why != "" {
		return nil, failure(-32000, why)
	}
	options := s.options
	options.Repo = p.Repo
	session, err := core.New(options)
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
	if err == nil {
		return s.details(owned.session), nil
	}
	if id == "" {
		return nil, err
	}
	if why := s.lockedKeyRefusal(); why != "" {
		return nil, failure(-32000, why)
	}
	options := s.options
	options.Repo = ""
	session, resumeErr := core.Resume(options, id)
	if resumeErr != nil {
		return nil, failure(-32000, resumeErr.Error())
	}
	s.own(session)
	return s.details(session), nil
}

func (s *server) info(req request, _ *peer) (any, *rpcError) {
	owned, _, err := s.session(req)
	if err != nil {
		return nil, err
	}
	return s.details(owned.session), nil
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
	owned.driver.Stop()
	owned.driver.Wait()
	closeErr := owned.session.Close()
	<-owned.drained
	delete(s.sessions, id)
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
	owned, _, err := s.session(req)
	if err != nil {
		return nil, err
	}
	if owned.driver.Active() {
		return nil, failure(-32000, "Stop the active run before inspecting friction")
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
	owned, ok := s.sessions[p.ID]
	if !ok {
		return nil, failure(-32002, "Session is not open in this daemon")
	}
	if owned.driver.Active() {
		return nil, failure(-32000, "Stop the active run before evaluating")
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
	owned, ok := s.sessions[p.ID]
	if !ok {
		return nil, failure(-32002, "Session is not open in this daemon")
	}
	if owned.driver.Active() {
		return nil, failure(-32000, "Stop the active run before sending a message")
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
	names := []string{"id", "after", "limit"}
	if withThrough {
		names = append(names, "through")
	}
	if err := params(req.Params, &p, names...); err != nil {
		return p, err
	}
	if p.ID == "" {
		return p, failure(-32602, "id is required")
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
	result, subErr := owned.stream.subscribe(client, s.store(), p.After, p.Limit)
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
	page, readErr := s.store().ReadLog(p.ID, p.After, p.Through, p.Limit)
	if readErr != nil {
		return nil, failure(-32000, readErr.Error())
	}
	return page, nil
}

func (s *server) own(session *core.Session, drivers ...*run.Driver) {
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
func (s *server) close() error {
	s.mu.Lock()
	for _, a := range s.authorizations {
		a.cancel()
	}
	s.mu.Unlock()
	var first error
	for id, owned := range s.sessions {
		owned.driver.Stop()
		owned.driver.Wait()
		if err := owned.session.Close(); err != nil && first == nil {
			first = err
		}
		<-owned.drained
		delete(s.sessions, id)
	}
	return first
}
