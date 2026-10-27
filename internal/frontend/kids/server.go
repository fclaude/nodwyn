package kids

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/frontend/web"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"golang.org/x/sys/unix"
)

//go:embed assets/* init.scm
var embedded embed.FS

// Profile is installed by the operator on the read-only container root.
// Limits can only be lowered; the model endpoint is the isolated proxy.
type Profile struct {
	Child        string `json:"child"`
	Host         string `json:"host"`
	Preview      string `json:"preview"`
	Public       string `json:"public"`
	Endpoint     string `json:"endpoint"`
	Model        string `json:"model"`
	State        string `json:"state"`
	Workspace    string `json:"workspace"`
	Export       string `json:"export"`
	Socket       string `json:"socket"`
	DailyTokens  int64  `json:"daily_tokens"`
	DailyMinutes int64  `json:"daily_minutes"`
}

type message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}
type conversation struct {
	session  *core.Session
	turn     string
	busy     bool
	messages []message
	reply    string
	outcome  string
	status   string
}

// Server holds no publishing credential and never exposes the ordinary RPC.
type Server struct {
	mu       sync.Mutex
	profile  Profile
	store    *Store
	provider llm.Provider
	sessions map[string]*conversation
	deletes  map[string]deletion
	init     string
	handler  http.Handler
}
type deletion struct {
	Project string
	Version uint64
	Expires time.Time
}

func New(profile Profile, upstream llm.Provider) (*Server, error) {
	if !slugPattern.MatchString(profile.Child) || !strings.HasPrefix(profile.Host, "https://") || !strings.HasPrefix(profile.Preview, "https://") || !strings.HasPrefix(profile.Public, "https://") || profile.Model == "" || profile.Endpoint != "http://model:11434" {
		return nil, errors.New("invalid managed profile")
	}
	for _, p := range []string{profile.State, profile.Workspace, profile.Export, profile.Socket} {
		if !filepath.IsAbs(p) {
			return nil, errors.New("managed paths must be absolute")
		}
	}
	store, err := OpenStore(profile.Workspace, profile.Export)
	if err != nil {
		return nil, err
	}
	if err = store.RecoverTurns(); err != nil {
		return nil, err
	}
	ledger, err := OpenLedger(profile.State, profile.DailyTokens, profile.DailyMinutes)
	if err != nil {
		return nil, err
	}
	if upstream == nil {
		upstream = llm.NewOllama(profile.Endpoint)
	}
	policy, err := embedded.ReadFile("init.scm")
	if err != nil {
		return nil, err
	}
	init := filepath.Join(profile.State, "managed-init.scm")
	if err = atomicFile(init, policy); err != nil {
		return nil, err
	}
	assets, err := fs.Sub(embedded, "assets")
	if err != nil {
		return nil, err
	}
	return &Server{profile: profile, store: store, provider: NewProvider(upstream, profile.Model, ledger), sessions: map[string]*conversation{}, deletes: map[string]deletion{}, init: init, handler: http.FileServer(http.FS(assets))}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/" && r.URL.Path != "/index.html" && r.URL.Path != "/kids.js" && r.URL.Path != "/kids.css" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.handler.ServeHTTP(w, r)
}
func answer(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusBadRequest) }

// ServeChild accepts a small, explicit method set. Paired child devices never
// receive log/RPC/setup objects; arbitrary JSON fields are refused as well.
func (s *Server) ServeChild(w http.ResponseWriter, r *http.Request, parent bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/kids/api/state" && r.Method == http.MethodGet {
		ps := s.store.Projects()
		sortedProjects(ps)
		view := []map[string]any{}
		for _, p := range ps {
			c := s.sessions[p.ID]
			messages := []message{}
			busy := false
			status := "Saved"
			if c != nil {
				messages = c.messages
				busy = c.busy
				status = c.status
			} else {
				_ = readJSON(s.chatPath(p.ID), &messages)
			}
			view = append(view, map[string]any{"id": p.ID, "name": p.Slug, "version": p.Version, "busy": busy, "messages": messages, "status": status, "preview": s.profile.Preview + "/" + p.Slug + "/", "public": s.profile.Public + "/" + p.Slug + "/"})
		}
		answer(w, map[string]any{"child": s.profile.Child, "parent": parent, "projects": view, "public": s.profile.Public})
		return
	}
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var b struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Text    string `json:"text"`
		Version uint64 `json:"version"`
		Confirm string `json:"confirm"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		problem(w, errors.New("invalid request"))
		return
	}
	switch r.URL.Path {
	case "/kids/api/create":
		p, err := s.store.Create(b.Name)
		if err != nil {
			problem(w, err)
		} else {
			answer(w, p)
		}
		return
	case "/kids/api/chat":
		if strings.TrimSpace(b.Text) == "" || len(b.Text) > 8000 {
			problem(w, errors.New("write a short message"))
			return
		}
		c, err := s.session(b.ID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "kids session:", err)
			problem(w, errors.New("your helper could not start; ask a parent"))
			return
		}
		if c.busy {
			problem(w, errors.New("your helper is already working on this project"))
			return
		}
		c.busy, c.status = true, "Building your app"
		c.turn = randomID()
		c.reply, c.outcome = "", ""
		c.messages = append(c.messages, message{Role: "user", Text: b.Text})
		if err = s.saveChat(b.ID, c); err == nil {
			err = c.session.Send(b.Text)
		}
		if err != nil {
			c.busy = false
			c.status = "Your helper could not start. Ask a parent."
			problem(w, errors.New(c.status))
			return
		}
		answer(w, map[string]bool{"accepted": true})
		return
	case "/kids/api/stop":
		if c := s.sessions[b.ID]; c != nil {
			c.session.Abort()
		}
		answer(w, map[string]bool{"stopped": true})
		return
	case "/kids/api/rename", "/kids/api/delete-check", "/kids/api/delete":
		if c := s.sessions[b.ID]; c != nil && c.busy {
			problem(w, errors.New("stop your helper and wait before changing the project"))
			return
		}
		if r.URL.Path == "/kids/api/delete-check" {
			found := false
			for _, p := range s.store.Projects() {
				if p.ID == b.ID && p.Version == b.Version {
					found = true
				}
			}
			if !found {
				problem(w, errors.New("project changed; refresh and try again"))
				return
			}
			for token, check := range s.deletes {
				if time.Now().After(check.Expires) {
					delete(s.deletes, token)
				}
			}
			if len(s.deletes) >= 64 {
				problem(w, errors.New("wait a moment before confirming another deletion"))
				return
			}
			token := randomID()
			s.deletes[token] = deletion{b.ID, b.Version, time.Now().Add(time.Minute)}
			answer(w, map[string]string{"confirm": token})
			return
		}
		op := "rename"
		if r.URL.Path == "/kids/api/delete" {
			check, ok := s.deletes[b.Confirm]
			delete(s.deletes, b.Confirm)
			if !ok || check.Project != b.ID || check.Version != b.Version || time.Now().After(check.Expires) {
				problem(w, errors.New("confirm this exact project again"))
				return
			}
			op = "delete"
		}
		if err := s.store.Change(b.ID, op, b.Name, "", b.Version); err != nil {
			problem(w, err)
			return
		}
		if op == "delete" {
			if c := s.sessions[b.ID]; c != nil {
				_ = c.session.Close()
				delete(s.sessions, b.ID)
			}
		}
		answer(w, map[string]bool{"saved": true})
		return
	default:
		http.NotFound(w, r)
	}
}
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (s *Server) chatPath(id string) string {
	return filepath.Join(s.profile.State, "chat", id+".json")
}
func (s *Server) saveChat(id string, c *conversation) error {
	if len(c.messages) > 200 {
		c.messages = append([]message{}, c.messages[len(c.messages)-200:]...)
	}
	b, err := json.Marshal(c.messages)
	if err != nil {
		return err
	}
	return atomicFile(s.chatPath(id), b)
}
func (s *Server) session(id string) (*conversation, error) {
	if c := s.sessions[id]; c != nil {
		return c, nil
	}
	var p Project
	found := false
	for _, q := range s.store.Projects() {
		if q.ID == id {
			p = q
			found = true
		}
	}
	if !found {
		return nil, errors.New("project not found")
	}
	cfg := config.Default()
	cfg.Control = config.Model{Provider: "ollama", URL: s.profile.Endpoint, Model: s.profile.Model, NumCtx: 32768, MaxOutput: 4096}
	cfg.Window = config.Window{HighWater: 20000, LowWater: 10000, MaxResultTokens: 2000, OutputReserve: 4096, Fixed: true, ResultsFixed: true}
	cfg.Loop.Profile = config.ProfileLean
	cfg.Loop.RequestTimeout = "2m"
	cfg.Approval = config.Approval{Writes: "allow", Commands: "ask"}
	cfg.Docs = config.Docs{}
	c := &conversation{status: "Saved", messages: []message{}}
	_ = readJSON(s.chatPath(id), &c.messages)
	root := config.CanonicalPath(filepath.Join(s.profile.Workspace, id))
	opts := core.Options{Config: cfg, Paths: config.PathsUnder(filepath.Join(s.profile.State, "config", id), filepath.Join(s.profile.State, "data", id)), Repo: root, Provider: s.provider, InitFile: s.init, Unattended: true, AutoApprove: true}
	// The profile's turn limits are its own, the same for every
	// conversation, and no conversation changes them.
	opts.Limits = &core.LimitsChoice{Limits: budget.Task{ToolCalls: 40, Actions: 20, Tokens: 200000, Wall: int64(8 * time.Minute)}, Actor: "managed"}
	opts.Managed = &core.ManagedProfile{Endpoint: s.profile.Endpoint, Model: s.profile.Model, WriteFile: func(path string, b []byte, mode os.FileMode) error {
		// The core pins paths to the physical root. The journal uses the
		// operator's spelling, which may reach it through a directory alias.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return s.store.Write(id, c.turn, filepath.Join(s.profile.Workspace, id, rel), b, mode)
	}}
	var err error
	if p.Task != "" {
		c.session, err = core.Resume(opts, p.Task)
	} else {
		c.session, err = core.New(opts)
	}
	if err != nil {
		return nil, err
	}
	if p.Task == "" {
		if err = s.store.Change(id, "task", "", c.session.ID(), p.Version); err != nil {
			_ = c.session.Close()
			return nil, err
		}
	}
	s.sessions[id] = c
	go s.events(id, c)
	return c, nil
}
func (s *Server) events(id string, c *conversation) {
	for e := range c.session.Events() {
		s.mu.Lock()
		switch e.Kind {
		case string(log.KindLLMResp):
			if resp, ok := e.Payload.(log.LLMResp); ok && len(resp.ToolCalls) == 0 {
				c.reply = resp.Content
			}
		case string(log.KindContextCompact):
			c.reply = ""
		case string(log.KindTurnEnd):
			if end, ok := e.Payload.(log.TurnEnd); ok {
				c.outcome = end.Outcome
			}
			if c.reply != "" {
				text := c.reply
				if len(text) > 16000 {
					text = text[:16000]
				}
				c.messages = append(c.messages, message{Role: "assistant", Text: text})
				c.reply = ""
				if err := s.saveChat(id, c); err != nil {
					c.status = "Could not save your conversation. Ask a parent."
				}
			}
		case event.Error:
			c.status = "Your helper stopped. Try a smaller change, or ask a parent."
			if info, ok := e.Payload.(event.ErrorInfo); ok {
				fmt.Fprintln(os.Stderr, "kids turn:", info.Err)
				if strings.Contains(info.Err, "finished for today") {
					c.status = "Building is finished for today. You can build again tomorrow."
				}
			}
		case event.Status:
			if info, ok := e.Payload.(event.StatusInfo); ok && info.State == "idle" && c.busy {
				c.busy = false
				if err := s.store.EndTurn(id, c.turn, c.outcome); err != nil {
					c.status = "Your changes need a parent to check their saved history."
				} else if c.status == "Building your app" {
					c.status = "Saved"
				}
			}
		}
		s.mu.Unlock()
	}
}

// Run holds an exclusive process lock and accepts only private Unix HTTP. The
// ingress sidecar owns HTTPS; it has no access to this state or workspace.
func Run(ctx context.Context, p Profile) error {
	if err := os.MkdirAll(p.State, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(p.State, "service.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("managed service is already running")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	s, err := New(p, nil)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p.Socket), 0o700); err != nil {
		return err
	}
	if err = os.Remove(p.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", p.Socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(p.Socket, 0o660); err != nil {
		return err
	}
	code, err := web.PairingCode()
	if err != nil {
		return err
	}
	devices := web.OpenDevices(filepath.Join(p.State, "devices.toml"))
	err = web.Serve(ctx, listener, web.Options{PairingCode: code, Devices: devices, Hosts: []string{p.Host}, Managed: s, Audit: os.Stderr})
	s.mu.Lock()
	sessions := []*core.Session{}
	for _, c := range s.sessions {
		sessions = append(sessions, c.session)
	}
	s.mu.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
	return err
}
