package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/container"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fakecontainer "github.com/fclaude/nodwyn/test/fakes/container"
	"github.com/fclaude/nodwyn/test/fakes/home"
	fakemcp "github.com/fclaude/nodwyn/test/fakes/mcp"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The test binary is also the fake container runtime (test/fakes/container):
// a contained server it runs offers these tools.
//
// A session with neither a LogWriter nor Paths logs to the working
// directory, which under go test is this package's. A run that leaves a log
// here fails, so a stray log is not committed with the change that made it.
func TestMain(m *testing.M) {
	fakecontainer.Main(containedTools()...)
	// A home of its own (test/fakes/home): nodwyn's denylist, git's
	// configuration and nodwyn's own read from it.
	done := home.Isolate()
	code := m.Run()
	done()
	if left, _ := filepath.Glob("*.jsonl"); len(left) != 0 {
		fmt.Fprintf(os.Stderr, "session logs left in internal/core: %v\ngive the session Paths under t.TempDir() or a LogWriter\n", left)
		code = 1
	}
	os.Exit(code)
}

func containedTools() []fakemcp.Tool {
	text := map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []any{"text"}}
	return []fakemcp.Tool{
		{Name: "search", Description: "Search the notes", Schema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []any{"q"}},
			Reply: func(args map[string]any) (string, bool) { return "2 notes about " + args["q"].(string), false }},
		{Name: "write_note", Description: "Write a note", Schema: text, Reply: func(map[string]any) (string, bool) { return "written", false }},
	}
}

const notesImage = "ghcr.io/example/mcp-notes@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// containedWorld is a workspace, a keychain holding notes-key, and a fake
// podman that has the notes image.
type containedWorld struct {
	t      *testing.T
	repo   string
	paths  config.Paths
	store  *keychain.Store
	fake   *fakecontainer.Runtime
	egress string
}

func newContainedWorld(t *testing.T) *containedWorld {
	t.Helper()
	root := t.TempDir()
	w := &containedWorld{t: t, repo: filepath.Join(root, "work"), paths: config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))}
	os.MkdirAll(filepath.Join(w.repo, "docs"), 0o755)
	os.MkdirAll(filepath.Join(w.repo, ".nodwyn"), 0o755)
	os.MkdirAll(w.paths.ConfigDir, 0o700)
	store, err := keychain.Create(filepath.Join(root, "keychain"), keychain.FastKDF, []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	store.Add("notes-key", []byte("sk-notes-canary-0123456789abcdef"))
	w.store = store
	w.fake = fakecontainer.New(t, "podman")
	w.fake.AddImage(notesImage)
	w.egress = filepath.Join(root, "nodwyn-egress")
	os.WriteFile(w.egress, []byte("#!/bin/false\n"), 0o755)
	t.Setenv("NODWYN_HOST_CANARY", "host-environment")
	return w
}

func (w *containedWorld) options() Options {
	cfg := config.Default()
	cfg.MCP.Egress = w.egress
	return Options{Config: cfg, Repo: w.repo, Paths: w.paths, Keychain: w.store, Version: "test", Containers: &container.Runtime{Kind: "podman", Path: w.fake.Path}}
}

func (w *containedWorld) server(network ...string) config.MCPServer {
	return config.MCPServer{Name: "notes", Transport: config.TransportContainer, Image: notesImage, Network: network, Mounts: []string{"docs"},
		Workspaces: []string{w.repo}, Tools: map[string]config.MCPTool{"search": {Effect: config.EffectRead, On: true}, "write_note": {On: true}}}
}

func (w *containedWorld) bind(host string) {
	w.t.Helper()
	row, err := binding.Exact("notes-key", host, "*", "env:NOTES_TOKEN")
	if err != nil {
		w.t.Fatal(err)
	}
	if err := binding.AppendExact(context.Background(), w.paths.Bindings, row); err != nil {
		w.t.Fatal(err)
	}
}

// approve reviews the server's tools as Connections does (a container is
// started for it and removed) and saves it approved.
func (w *containedWorld) approve(server config.MCPServer) config.MCPServer {
	w.t.Helper()
	tools, manifest, err := InspectMCP(w.options(), server)
	if err != nil {
		w.t.Fatal(err)
	}
	if len(tools) != 2 || len(w.fake.Running()) != 0 {
		w.t.Fatalf("inspection: %d tools, still running %v", len(tools), w.fake.Running())
	}
	server.Manifest = manifest
	w.save(server)
	return server
}

func (w *containedWorld) save(server config.MCPServer) {
	w.t.Helper()
	if err := config.SaveMCPServer(config.MCPFile(w.paths), server); err != nil {
		w.t.Fatal(err)
	}
}

func (w *containedWorld) session(turns ...fake.Turn) *rig {
	w.t.Helper()
	srv := fake.NewT(w.t, turns...)
	w.t.Cleanup(srv.Close)
	o := w.options()
	o.Config.Control.URL, o.Config.Control.Model = srv.URL, "test-model"
	o.Provider = llm.NewOllama(srv.URL)
	logb := &bytes.Buffer{}
	o.LogWriter = logb
	s, err := New(o)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { s.Close() })
	return &rig{t: w.t, s: s, srv: srv, repo: w.repo, logb: logb}
}

// serverRuns are the runs of a server, not of its proxy.
func (w *containedWorld) serverRuns() []fakecontainer.Invocation {
	var out []fakecontainer.Invocation
	for _, inv := range w.fake.Find("run", "-i") {
		if !slices.Contains(inv.Args, "--entrypoint=/nodwyn-egress") {
			out = append(out, inv)
		}
	}
	return out
}

// noHostEnvironment fails if anything of nodwyn's own environment reached
// the runtime beyond what it needs.
func (w *containedWorld) noHostEnvironment() {
	w.t.Helper()
	for _, inv := range w.fake.Calls() {
		for _, kv := range inv.Env {
			if strings.HasPrefix(kv, "NODWYN_HOST_CANARY=") {
				w.t.Fatalf("the host environment reached %q", inv.Args)
			}
		}
	}
}

// A contained server with no network: its tools are offered once approved,
// a read is gathered, an undeclared tool asks naming the contained server,
// both calls reach it over stdio, and closing the session removes it.
func TestContainedServerCrossesTheGuard(t *testing.T) {
	w := newContainedWorld(t)
	w.approve(w.server())
	r := w.session(
		fake.Turn{ToolCalls: []fake.ToolCall{
			{ID: "s", Name: "notes__search", Args: map[string]any{"q": "status"}},
			{ID: "w", Name: "notes__write_note", Args: map[string]any{"text": "Weekly status"}},
		}},
		fake.Turn{Content: "noted"},
	)
	status := r.s.MCP()
	if len(status) != 1 || status[0].State != "ready" || status[0].Transport != "container" || status[0].Host != "mcp:notes" || strings.Join(status[0].Tools, ",") != "notes__search,notes__write_note" {
		t.Fatalf("status: %+v", status)
	}
	r.s.Send("note the status")
	var confirms []event.Confirm
	for _, e := range r.drain("y") {
		if e.Kind == event.PromptConfirm {
			confirms = append(confirms, e.Payload.(event.Confirm))
		}
	}
	if len(confirms) != 1 || confirms[0].Prompt.Prim != "notes__write_note" || confirms[0].Prompt.Egress != nil {
		t.Fatalf("confirms: %+v", confirms)
	}
	if !slices.ContainsFunc(confirms[0].Prompt.Reasons, func(r string) bool { return strings.Contains(r, "the contained server notes (network: none)") }) {
		t.Fatalf("the confirm does not name the contained server: %q", confirms[0].Prompt.Reasons)
	}
	if o := confirms[0].Options; o == nil || o.Session || o.Turn {
		t.Fatalf("an undeclared tool offered a standing answer: %+v", o)
	}
	runs := w.serverRuns()
	if len(runs) != 2 { // the inspection's, then the session's
		t.Fatalf("runs: %d", len(runs))
	}
	args := runs[1].Args
	docs, _ := filepath.EvalSymlinks(filepath.Join(w.repo, "docs"))
	for _, want := range []string{"--network=none", "--pull=never", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--volume=" + docs + ":/workspace/docs:ro,z", "--name=nodwyn-notes-" + strings.ToLower(r.s.ID()), notesImage} {
		if !slices.Contains(args, want) {
			t.Errorf("run lacks %s: %q", want, args)
		}
	}
	if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--env") }) {
		t.Fatalf("a server without a credential got a variable: %q", args)
	}
	w.noHostEnvironment()
	r.s.Close()
	if running := w.fake.Running(); len(running) != 0 {
		t.Fatalf("still running after close: %v", running)
	}
	if len(w.fake.Find("rm", "--force", "--ignore", "--time=0", "nodwyn-notes-"+strings.ToLower(r.s.ID()))) != 1 {
		t.Fatal("the container was not removed")
	}
	calls := w.fake.ToolCalls()
	if len(calls) != 2 || calls[0].Name != "search" || calls[1].Args["text"] != "Weekly status" {
		t.Fatalf("tool calls: %+v", calls)
	}
	results := 0
	for _, ev := range r.logged() {
		var res log.ToolResult
		if ev.Kind == log.KindToolResult && ev.Decode(&res) == nil && res.OK {
			results++
		}
	}
	if results != 2 {
		t.Fatalf("results: %d", results)
	}
}

// A contained server's result is external:mcp, and content from outside
// may go to it: the person approved it and its network list.
func TestContainedResultsAreExternal(t *testing.T) {
	w := newContainedWorld(t)
	w.approve(w.server())
	r := w.session()
	os.WriteFile(filepath.Join(w.repo, "notes.txt"), []byte("status"), 0o644)
	if got := r.eval(`(taint-of (notes__search (fs-read "notes.txt")))`); got != `("external:file" "external:mcp")` {
		t.Fatalf("taint: %s", got)
	}
}

// A server not yet approved offers nothing and does not keep running.
func TestContainedServerUnapprovedStops(t *testing.T) {
	w := newContainedWorld(t)
	w.save(w.server())
	r := w.session()
	if s := r.s.MCP(); len(s) != 1 || s[0].State != "unapproved" || len(s[0].Tools) != 0 {
		t.Fatalf("status: %+v", s)
	}
	if running := w.fake.Running(); len(running) != 0 {
		t.Fatalf("an unapproved server kept running: %v", running)
	}
}

// The credential goes in only if its class is bound for every host on the
// network list at its env: position; each lookup is recorded. Then the
// proxy runs first, the server reaches only it, the key is in the
// runtime's environment and never in an argument, and what the proxy
// reports is in the session log.
func TestContainedCredentialMustBeBoundForEveryHost(t *testing.T) {
	w := newContainedWorld(t)
	server := w.server("api.notes.example", "files.notes.example:8443")
	server.Credential, server.Position = "notes-key", "env:NOTES_TOKEN"
	w.bind("api.notes.example")
	server = w.approveUnkeyed(server)

	r := w.session()
	if s := r.s.MCP(); len(s) != 1 || s[0].State != "error" || !strings.Contains(s[0].Detail, "not started") || !strings.Contains(s[0].Detail, "files.notes.example") {
		t.Fatalf("half-bound: %+v", s)
	}
	if len(w.serverRuns()) != 1 { // the inspection's alone
		t.Fatal("a server whose key is not bound everywhere was started")
	}
	r.s.Close()
	var checks []log.EgressCheck
	for _, ev := range r.logged() {
		var c log.EgressCheck
		if ev.Kind == log.KindEgressCheck && ev.Decode(&c) == nil {
			checks = append(checks, c)
		}
	}
	if len(checks) != 2 || !checks[0].Bound || checks[1].Bound || checks[1].Host != "files.notes.example" || checks[1].Prim != "notes__*" {
		t.Fatalf("checks: %+v", checks)
	}

	w.bind("files.notes.example")
	w.fake.SetReports(`{"method":"CONNECT","host":"api.notes.example","port":443,"allowed":true}`, `{"method":"CONNECT","host":"evil.example.net","port":443,"allowed":false,"reason":"not on the server's network list"}`)
	r = w.session(
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "s", Name: "notes__search", Args: map[string]any{"q": "plans"}}}},
		fake.Turn{Content: "found"},
	)
	if s := r.s.MCP(); len(s) != 1 || s[0].State != "ready" || strings.Join(s[0].Network, ",") != "api.notes.example,files.notes.example" {
		t.Fatalf("bound: %+v", s)
	}
	runs := w.serverRuns()
	run := runs[len(runs)-1]
	if !slices.Contains(run.Env, "NOTES_TOKEN=sk-notes-canary-0123456789abcdef") || !slices.Contains(run.Args, "--env=NOTES_TOKEN") || !slices.Contains(run.Args, "--network=nodwyn-notes-"+strings.ToLower(r.s.ID())+"-in") {
		t.Fatalf("server run: %q", run.Args)
	}
	sidecar := w.fake.Find("run", "-i", "--name=nodwyn-notes-"+strings.ToLower(r.s.ID())+"-egress")
	if len(sidecar) != 1 || !slices.Contains(sidecar[0].Args, "-allow=api.notes.example") || !slices.Contains(sidecar[0].Args, "-allow=files.notes.example:8443") {
		t.Fatalf("proxy run: %+v", sidecar)
	}
	for _, inv := range w.fake.Calls() {
		if strings.Contains(strings.Join(inv.Args, " "), "canary") {
			t.Fatalf("the key is in an argument list: %q", inv.Args)
		}
	}
	w.noHostEnvironment()
	r.s.Send("look")
	r.drain("y")
	// A binding removed during the session stops further calls.
	os.WriteFile(w.paths.Bindings, nil, 0o600)
	if _, err := r.s.Eval(context.Background(), `(notes__search "again")`); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("after the binding went: %v", err)
	}
	r.s.Close()
	if calls := w.fake.ToolCalls(); len(calls) != 1 || calls[0].Args["q"] != "plans" {
		t.Fatalf("tool calls: %+v", calls)
	}
	if strings.Contains(r.logb.String(), "canary") {
		t.Fatal("the key reached the log")
	}
	evs := r.logged()
	start := slices.IndexFunc(evs, func(ev log.Event) bool { return ev.Kind == log.KindSessionStart })
	var egress []log.MCPEgress
	for i, ev := range evs {
		var e log.MCPEgress
		if ev.Kind == log.KindMCPEgress && ev.Decode(&e) == nil {
			if i < start {
				t.Fatal("an egress record before session.start")
			}
			egress = append(egress, e)
		}
	}
	if len(egress) != 2 || egress[0] != (log.MCPEgress{Server: "notes", Method: "CONNECT", Host: "api.notes.example", Port: 443, Allowed: true}) || egress[1].Allowed || egress[1].Host != "evil.example.net" {
		t.Fatalf("egress records: %+v", egress)
	}
	if evs[len(evs)-1].Kind != log.KindSessionEnd {
		t.Fatal("records after session.end")
	}
}

// approveUnkeyed reviews a keyed server before all its bindings exist:
// inspection injects the key only where it is bound everywhere, so the
// review runs without the credential.
func (w *containedWorld) approveUnkeyed(server config.MCPServer) config.MCPServer {
	w.t.Helper()
	if _, _, err := InspectMCP(w.options(), server); err == nil || !strings.Contains(err.Error(), "not bound") {
		w.t.Fatalf("inspection injected a key not bound everywhere: %v", err)
	}
	unkeyed := server
	unkeyed.Credential, unkeyed.Position = "", ""
	approved := w.approve(unkeyed)
	server.Manifest = approved.Manifest
	w.save(server)
	return server
}

// Without its image, or without a runtime, a contained server is
// unavailable and says what to do; a remote server in the same workspace
// still works.
func TestContainedServerUnavailable(t *testing.T) {
	w := newContainedWorld(t)
	server := w.approve(w.server())
	remote := fakemcp.New(mcpTools()...)
	defer remote.Close()
	remoteTools, remoteManifest, err := InspectMCP(w.options(), config.MCPServer{Name: "chat", URL: remote.URL})
	if err != nil || len(remoteTools) != 3 {
		t.Fatal(err)
	}
	w.save(config.MCPServer{Name: "chat", URL: remote.URL, Manifest: remoteManifest, Workspaces: []string{w.repo}, Tools: map[string]config.MCPTool{"search": {Effect: config.EffectRead, On: true}}})

	server.Image = strings.Replace(notesImage, "0123", "4567", 1)
	w.save(server)
	r := w.session()
	byName := map[string]MCPServer{}
	for _, s := range r.s.MCP() {
		byName[s.Name] = s
	}
	if s := byName["notes"]; s.State != "unavailable" || !strings.Contains(s.Detail, "nodwyn mcp pull notes") {
		t.Fatalf("no image: %+v", s)
	}
	if byName["chat"].State != "ready" {
		t.Fatalf("remote: %+v", byName["chat"])
	}
	if len(w.fake.Find("pull")) != 0 {
		t.Fatal("a session pulled an image")
	}
	r.s.Close()

	t.Setenv("PATH", t.TempDir())
	o := w.options()
	o.Containers = nil
	if _, err := ContainerRuntime(o); err == nil {
		t.Fatal("found a runtime on an empty PATH")
	}
	srv := fake.NewT(t)
	defer srv.Close()
	o.Config.Control.URL, o.Config.Control.Model, o.Provider, o.LogWriter = srv.URL, "test-model", llm.NewOllama(srv.URL), &bytes.Buffer{}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, st := range s.MCP() {
		if st.Name == "notes" && (st.State != "unavailable" || !strings.Contains(st.Detail, "neither podman nor docker")) {
			t.Fatalf("no runtime: %+v", st)
		}
		if st.Name == "chat" && st.State != "ready" {
			t.Fatalf("remote without a runtime: %+v", st)
		}
	}
}

// Mounts resolve inside the workspace, following links, and never reach
// .nodwyn/ or beyond.
func TestContainedMounts(t *testing.T) {
	w := newContainedWorld(t)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(w.repo, "escape"))
	os.Symlink(filepath.Join(w.repo, ".nodwyn"), filepath.Join(w.repo, "policy"))
	os.MkdirAll(filepath.Join(w.repo, "nested", ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(w.repo, "file.txt"), nil, 0o644)
	for _, m := range []string{"escape", "policy", "nested", "file.txt", "missing"} {
		server := w.server()
		server.Mounts = []string{m}
		if _, err := containedMounts(w.repo, server); err == nil {
			t.Errorf("mounted %s", m)
		}
	}
	server := w.server()
	server.Mounts = []string{"docs:write"}
	mounts, err := containedMounts(w.repo, server)
	if err != nil || len(mounts) != 1 || !mounts[0].Write || mounts[0].Target != "/workspace/docs" {
		t.Fatalf("%+v %v", mounts, err)
	}
}
