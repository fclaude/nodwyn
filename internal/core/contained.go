package core

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/container"
	"github.com/fclaude/nodwyn/internal/egress"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/mcp"
	"github.com/fclaude/nodwyn/internal/redact"
)

// Contained MCP servers (ADR 23 W3b): nodwyn runs the server in a
// container from an image pinned by digest that is already here, with no
// host environment, no capability and only the workspace folders the
// person listed; with no network list it has no network at all, and with
// one its only way out is nodwyn's egress proxy, which lets through those
// hosts alone and reports every attempt (mcp.egress). The MCP conversation
// is the container's standard input and output. A credential goes in as
// one environment variable, and only if its class is bound for every host
// on the list at that position. The container is removed when the session
// closes.

// containedStartTimeout bounds starting the containers and reading the
// tool list: a cold start of a server's runtime can take a while.
const containedStartTimeout = 60 * time.Second

// maxEgressRecords is how many proxy reports a session records for one
// server; the rest are counted.
const maxEgressRecords = 1000

// containedDest is the destination a contained server's tools send to, as
// the guard names it: the container, mcp:NAME (ADR 22 §4), not a host.
func containedDest(server config.MCPServer) string { return "mcp:" + server.Name }

// containedPhrase says where a contained server's arguments go, for a
// confirm and the tool's description.
func containedPhrase(server config.MCPServer) string {
	network := "none"
	if hosts := server.NetworkHosts(); len(hosts) > 0 {
		network = strings.Join(hosts, ", ")
	}
	return "the contained server " + server.Name + " (network: " + network + ")"
}

// ContainerRuntime finds the runtime contained servers run with: the one
// the options give (tests, embedders), or the one config.toml names, or
// podman, or docker.
func ContainerRuntime(o Options) (container.Runtime, error) {
	if o.Containers != nil {
		return *o.Containers, nil
	}
	return container.Find(o.Config.MCP.Runtime)
}

// EgressBinary finds nodwyn-egress, the proxy a contained server with a
// network list reaches the network through: [mcp] egress in config.toml,
// or next to the nodwyn executable (in the macOS app, the one built for
// the virtual machine's Linux, nodwyn-egress-linux-ARCH).
func EgressBinary(c config.MCP) (string, error) {
	candidates := []string{c.Egress}
	if c.Egress == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		dir := filepath.Dir(exe)
		candidates = []string{filepath.Join(dir, "nodwyn-egress"), filepath.Join(dir, "nodwyn-egress-linux-"+runtime.GOARCH)}
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, nil
		}
	}
	return "", fmt.Errorf("nodwyn-egress is not at %s: make build puts it next to nodwyn, or set [mcp] egress in config.toml", candidates[0])
}

// containedMounts resolves a server's mounts in the workspace at repo:
// each must be a folder strictly inside it, symbolic links followed, and
// neither .nodwyn/ nor a folder holding one.
func containedMounts(repo string, server config.MCPServer) ([]container.Mount, error) {
	if len(server.Mounts) == 0 {
		return nil, nil
	}
	if repo == "" {
		return nil, errors.New("its mounts are workspace folders: offer it in a workspace first")
	}
	root, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return nil, err
	}
	var out []container.Mount
	for _, m := range server.MountList() {
		src, err := filepath.EvalSymlinks(filepath.Join(root, m.Path))
		if err != nil {
			return nil, fmt.Errorf("mount %s: %w", m.Path, err)
		}
		rel, err := filepath.Rel(root, src)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("mount %s leads outside the workspace", m.Path)
		}
		if first := strings.Split(filepath.ToSlash(rel), "/")[0]; first == ".nodwyn" {
			return nil, fmt.Errorf("mount %s leads into .nodwyn/, which is never mounted", m.Path)
		}
		if info, err := os.Stat(src); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("mount %s is not a folder", m.Path)
		}
		if _, err := os.Stat(filepath.Join(src, ".nodwyn")); err == nil {
			return nil, fmt.Errorf("mount %s holds a .nodwyn/ folder, which is never mounted", m.Path)
		}
		if strings.ContainsAny(src, ":,") {
			return nil, fmt.Errorf("mount %s: its path has a colon or a comma, which a container runtime cannot mount", m.Path)
		}
		out = append(out, container.Mount{Source: src, Target: "/workspace/" + filepath.ToSlash(m.Path), Write: m.Write})
	}
	return out, nil
}

// containedStart is how one contained server is started: by a session, or
// for inspection from Connections.
type containedStart struct {
	runtime container.Runtime
	config  config.MCP
	dataDir string
	repo    string
	name    string // the container's name
	labels  map[string]string
	inject  func(*exec.Cmd) error // the credential, nil without one
	report  func(egress.Report)
}

// startContained runs the server and opens its MCP conversation. A
// session's tool calls and the inspection's tools/list go over it.
func startContained(ctx context.Context, server config.MCPServer, o containedStart) (*container.Server, *mcp.Client, error) {
	mounts, err := containedMounts(o.repo, server)
	if err != nil {
		return nil, nil, err
	}
	opts := container.Options{
		Spec: container.Spec{Name: o.name, Image: server.Image, Command: server.Command, Args: server.Args, Mounts: mounts,
			Network: server.Network, Labels: o.labels, SecretEnv: config.EnvName(server.Position)},
		EgressDir: filepath.Join(o.dataDir, "egress"),
		Inject:    o.inject,
		Report:    o.report,
	}
	if server.Credential == "" {
		opts.SecretEnv = ""
	}
	if len(server.Network) > 0 {
		if opts.Egress, err = EgressBinary(o.config); err != nil {
			return nil, nil, unavailable{err}
		}
	}
	c, err := o.runtime.Start(ctx, opts)
	if errors.Is(err, container.ErrImageMissing) {
		return nil, nil, unavailable{fmt.Errorf("its image is not on this computer: pull it with `nodwyn mcp pull %s` or from Connections (a task never downloads one)", server.Name)}
	}
	if err != nil {
		return nil, nil, err
	}
	return c, mcp.NewStdio(c.Stdin(), c.Stdout(), c.Ended), nil
}

// unavailable is a server that cannot run here as things are: something to
// install or pull, not something that went wrong.
type unavailable struct{ error }

func (u unavailable) Unwrap() error { return u.error }

// stderrDetail is the end of what a server wrote to its standard error,
// redacted, for the session's details: it is never given to the model.
func stderrDetail(c *container.Server) string {
	text := strings.TrimSpace(c.Stderr())
	if text == "" {
		return ""
	}
	return " (its last words: " + redact.Redact(text, nil).Text + ")"
}

// containedServer is a running contained server the session closes.
type containedServer struct {
	server *container.Server
	egress *egressRecorder
}

func (c containedServer) close() {
	c.server.Close()
	c.egress.close()
}

// egressRecorder records a server's proxy reports, up to a limit.
type egressRecorder struct {
	s       *Session
	server  string
	mu      sync.Mutex
	n       int
	dropped int
}

func (r *egressRecorder) report(rep egress.Report) {
	r.mu.Lock()
	r.n++
	over := r.n > maxEgressRecords
	if over {
		r.dropped++
	}
	r.mu.Unlock()
	if over {
		return
	}
	host := rep.Host
	if len(host) > 255 {
		host = host[:255] + "…"
	}
	r.s.recordEarly(log.KindMCPEgress, log.MCPEgress{Server: r.server, Method: rep.Method, Host: host, Port: rep.Port, Allowed: rep.Allowed, Reason: rep.Reason})
}

// close records how many reports were left out, once the proxy is gone.
func (r *egressRecorder) close() {
	r.mu.Lock()
	dropped := r.dropped
	r.dropped = 0
	r.mu.Unlock()
	if dropped > 0 {
		r.s.recordEarly(log.KindMCPEgress, log.MCPEgress{Server: r.server, Dropped: dropped, Reason: fmt.Sprintf("%d more attempts were not recorded", dropped)})
	}
}

// connectContained starts a contained server for this session, checks its
// manifest and registers the tools the person turned on. A server whose
// tools are not offered is stopped at once.
func (s *Session) connectContained(server config.MCPServer) MCPServer {
	status := MCPServer{Name: server.Name, Host: containedDest(server), Transport: config.TransportContainer, Network: server.NetworkHosts(), Tools: []string{}}
	fail := func(state string, err error) MCPServer {
		status.State, status.Detail = state, err.Error()
		return status
	}
	rt, err := ContainerRuntime(s.opts)
	if err != nil {
		return fail("unavailable", err)
	}
	var inject func(*exec.Cmd) error
	if server.Credential != "" {
		if inject, err = s.containedCredential(server); err != nil {
			return fail("error", err)
		}
	}
	recorder := &egressRecorder{s: s, server: server.Name}
	ctx, cancel := context.WithTimeout(context.Background(), containedStartTimeout)
	defer cancel()
	c, client, err := startContained(ctx, server, containedStart{
		runtime: rt, config: s.opts.Config.MCP, dataDir: s.opts.Paths.DataDir, repo: s.opts.Repo,
		name:   "nodwyn-" + server.Name + "-" + strings.ToLower(s.id),
		labels: map[string]string{"nodwyn.session": s.id, "nodwyn.mcp": server.Name},
		inject: inject, report: recorder.report,
	})
	if err != nil {
		state := "error"
		if errors.As(err, new(unavailable)) {
			state = "unavailable"
		}
		return fail(state, err)
	}
	running := containedServer{server: c, egress: recorder}
	var before func() error
	if server.Credential != "" {
		before = func() error { return s.recheckContained(server) }
	}
	status = s.offer(ctx, server, client, status, before)
	if status.State != "ready" {
		if status.State == "error" {
			status.Detail += stderrDetail(c)
		}
		running.close()
		return status
	}
	s.mcpMu.Lock()
	s.contained = append(s.contained, running)
	s.mcpMu.Unlock()
	return status
}

// containedCredential checks a contained server's credential against the
// binding table for every host on its network list, at its env: position,
// and records each check; it returns how the value is put in the
// runtime's environment. A server whose credential is not bound for one of
// its hosts is not started: it could carry the key there.
func (s *Session) containedCredential(server config.MCPServer) (func(*exec.Cmd) error, error) {
	if s.keychain == nil {
		return nil, errors.New("its credential is in the keychain, which is locked: unlock it and start a new task")
	}
	h := s.HandleForClass(server.Credential)
	if h == nil {
		return nil, fmt.Errorf("no %s credential in the keychain", server.Credential)
	}
	if err := s.checkContained(server, h.ID); err != nil {
		return nil, fmt.Errorf("not started: %w", err)
	}
	name := config.EnvName(server.Position)
	return func(cmd *exec.Cmd) error {
		// An environment variable cannot be refreshed later: the server
		// gets the token that is valid now.
		if err := s.freshCtx(context.Background(), h.ID); err != nil {
			return err
		}
		return s.keychain.InjectEnv(h.ID, name, cmd)
	}, nil
}

// checkContained looks up the credential's class for every host of the
// server at its position, recording each lookup, and fails at the first
// that is not bound.
func (s *Session) checkContained(server config.MCPServer, id string) error {
	if err := s.bindings.Refresh(); err != nil {
		return err
	}
	scopes := s.credentialScopes(server.Credential, id)
	for _, host := range server.NetworkHosts() {
		bound, reason := s.bindings.Lookup(server.Credential, host, "", server.Position, scopes...)
		s.recordEarly(log.KindEgressCheck, log.EgressCheck{Prim: config.ToolName(server.Name, "*"), Class: server.Credential, Host: host, Scopes: scopes, Bound: bound, Reason: reason})
		if !bound {
			return fmt.Errorf("its credential %s is not bound for %s at %s: %s", server.Credential, host, server.Position, reason)
		}
	}
	return nil
}

// recheckContained runs before every call of a keyed contained server: the
// container holds the key it was given at start, but nodwyn asks nothing
// of it once a binding no longer allows the key everywhere it can go.
func (s *Session) recheckContained(server config.MCPServer) error {
	h := s.HandleForClass(server.Credential)
	if h == nil {
		return fmt.Errorf("no %s credential in the keychain", server.Credential)
	}
	return s.checkContained(server, h.ID)
}

// closeMCP stops the session's contained servers and records how many
// proxy reports were left out. It runs before session.end.
func (s *Session) closeMCP() {
	s.mcpMu.Lock()
	running := s.contained
	s.contained = nil
	s.mcpMu.Unlock()
	var wg sync.WaitGroup
	for _, c := range running {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.close()
		}()
	}
	wg.Wait()
}

// inspectContained runs a contained server outside any session, as a
// session would, to read its tools for review. Its credential goes in only
// where the binding table allows it everywhere the server can reach; no
// check and no proxy report is recorded (there is no session log).
func inspectContained(o Options, server config.MCPServer) ([]mcp.Tool, string, error) {
	rt, err := ContainerRuntime(o)
	if err != nil {
		return nil, "", err
	}
	var inject func(*exec.Cmd) error
	if server.Credential != "" {
		if o.Keychain == nil {
			return nil, "", errors.New("unlock the keychain first: this server's credential is kept there")
		}
		h := o.Keychain.HandleForClass(server.Credential)
		if h == nil {
			return nil, "", fmt.Errorf("no %s credential in the keychain", server.Credential)
		}
		table, err := binding.Load(o.Paths.Bindings)
		if err != nil {
			return nil, "", err
		}
		info, err := o.Keychain.Info(h.ID)
		if err != nil {
			return nil, "", err
		}
		for _, host := range server.NetworkHosts() {
			if bound, reason := table.Lookup(server.Credential, host, "", server.Position, info.Scopes...); !bound {
				return nil, "", fmt.Errorf("its credential %s is not bound for %s at %s: %s", server.Credential, host, server.Position, reason)
			}
		}
		inject = func(cmd *exec.Cmd) error {
			if err := o.Keychain.Refresh(context.Background(), h.ID, nil); err != nil {
				return err
			}
			return o.Keychain.InjectEnv(h.ID, config.EnvName(server.Position), cmd)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), containedStartTimeout)
	defer cancel()
	c, client, err := startContained(ctx, server, containedStart{
		runtime: rt, config: o.Config.MCP, dataDir: o.Paths.DataDir, repo: inspectionRoot(server),
		name:   "nodwyn-" + server.Name + "-inspect-" + strings.ToLower(rand.Text()[:8]),
		labels: map[string]string{"nodwyn.mcp": server.Name, "nodwyn.inspect": "true"},
		inject: inject,
	})
	if err != nil {
		return nil, "", err
	}
	defer c.Close()
	if _, err := client.Initialize(ctx); err != nil {
		return nil, "", fmt.Errorf("%w%s", err, stderrDetail(c))
	}
	tools, err := client.Tools(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("%w%s", err, stderrDetail(c))
	}
	return tools, mcp.Manifest(tools), nil
}

// inspectionRoot is the workspace an inspection mounts from: the first
// the server is offered in, or none.
func inspectionRoot(server config.MCPServer) string {
	if len(server.Workspaces) > 0 {
		return server.Workspaces[0]
	}
	return ""
}
