package core

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/mcp"
	fakemcp "github.com/fclaude/nodwyn/test/fakes/mcp"
	fakeoauth "github.com/fclaude/nodwyn/test/fakes/oauth"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func mcpTools() []fakemcp.Tool {
	return []fakemcp.Tool{
		{Name: "search", Description: "Search messages", Schema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, "required": []any{"q"}},
			Reply: func(args map[string]any) (string, bool) { return "3 messages about " + args["q"].(string), false }},
		{Name: "post_message", Description: "Post to a channel", Schema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}, "attachment": map[string]any{"type": "object"}}, "required": []any{"text"}},
			Reply: func(args map[string]any) (string, bool) { return "posted", false }},
		{Name: "delete_everything", Description: "Delete", Reply: func(map[string]any) (string, bool) { return "gone", false }},
	}
}

// mcpRig starts a session with the fake approved (or not) as "chat". A
// keyed server has a credential bound to its host; a keyless one has
// neither, so no binding names its host.
func mcpRig(t *testing.T, server *fakemcp.Server, approve, keyed bool, after func(), turns ...fake.Turn) (*rig, config.Paths) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "work")
	os.MkdirAll(repo, 0o755)
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	os.MkdirAll(paths.ConfigDir, 0o700)
	store, err := keychain.Create(filepath.Join(root, "keychain"), keychain.FastKDF, []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	store.Add("chat-key", []byte("sk-mcp-canary-0123456789abcdef"))
	credential, position := "", ""
	if keyed {
		credential, position = "chat-key", "header:Authorization:Bearer"
		row, _ := binding.Exact(credential, mcpHost(server), "POST", position)
		if err := binding.AppendExact(context.Background(), paths.Bindings, row); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := mcp.New(server.URL, nil)
	c.Initialize(context.Background())
	tools, _ := c.Tools(context.Background())
	manifest := ""
	if approve {
		manifest = mcp.Manifest(tools)
	}
	if err := config.SaveMCPServer(config.MCPFile(paths), config.MCPServer{Name: "chat", URL: server.URL, Credential: credential, Position: position, Manifest: manifest,
		Workspaces: []string{repo}, Tools: map[string]config.MCPTool{"search": {Effect: config.EffectRead, On: true}, "post_message": {On: true}, "delete_everything": {On: false}}}); err != nil {
		t.Fatal(err)
	}
	if after != nil {
		after()
	}
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "test-model"
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: repo, Paths: paths, Keychain: store, Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: repo, logb: logb}, paths
}

func mcpHost(server *fakemcp.Server) string {
	host := strings.TrimPrefix(server.URL, "http://")
	return host[:strings.LastIndex(host, ":")]
}

// mcpDecisions are the guard's recorded decisions on prim.
func (r *rig) mcpDecisions(prim string) []log.GuardDecision {
	r.t.Helper()
	var out []log.GuardDecision
	for _, ev := range r.logged() {
		var d log.GuardDecision
		if ev.Kind != log.KindGuardDecision || ev.Decode(&d) != nil || d.Prim != prim {
			continue
		}
		out = append(out, d)
	}
	return out
}

func TestMCPToolsCrossTheGuard(t *testing.T) {
	server := fakemcp.New(mcpTools()...)
	defer server.Close()
	r, _ := mcpRig(t, server, true, true, nil,
		fake.Turn{ToolCalls: []fake.ToolCall{
			{ID: "s", Name: "chat__search", Args: map[string]any{"q": "status"}},
			{ID: "p", Name: "chat__post_message", Args: map[string]any{"text": "Weekly status"}},
		}},
		fake.Turn{Content: "posted"},
	)
	status := r.s.MCP()
	if len(status) != 1 || status[0].State != "ready" || strings.Join(status[0].Tools, ",") != "chat__post_message,chat__search" {
		t.Fatalf("status: %+v", status)
	}
	r.s.Send("post the status")
	evs := r.drain("y")
	var confirms []event.Confirm
	for _, e := range evs {
		if e.Kind == event.PromptConfirm {
			confirms = append(confirms, e.Payload.(event.Confirm))
		}
	}
	if len(confirms) != 1 || confirms[0].Prompt.Prim != "chat__post_message" || confirms[0].Prompt.Egress == nil || confirms[0].Prompt.Egress.Class != "chat-key" {
		t.Fatalf("confirms: %+v", confirms)
	}
	if o := confirms[0].Options; o == nil || o.Session || o.Turn {
		t.Fatalf("an undeclared tool offered a standing answer: %+v", o)
	}
	if d := confirms[0].Prompt.Detail; d != "text: Weekly status" {
		t.Fatalf("preview: %q", d)
	}
	if len(server.Calls) != 2 || server.Calls[0].Auth != "Bearer sk-mcp-canary-0123456789abcdef" || server.Calls[1].Args["text"] != "Weekly status" {
		t.Fatalf("calls: %+v", server.Calls)
	}
	if _, ok := server.Calls[0].Args["limit"]; ok {
		t.Fatal("sent an optional argument the model skipped")
	}
	if _, ok := server.Calls[1].Args["attachment"]; ok {
		t.Fatal("sent an optional argument the model skipped")
	}
	raw := string(r.srv.Requests[0].Raw)
	if !strings.Contains(raw, "chat__search") || strings.Contains(raw, "delete_everything") {
		t.Fatal("the model was offered the wrong tools")
	}
	if strings.Contains(r.logb.String(), "canary") || strings.Contains(string(r.srv.Requests[1].Raw), "canary") {
		t.Fatal("the credential reached the log or the model")
	}
	if !strings.Contains(r.logb.String(), `"kind":"egress.check"`) {
		t.Fatal("egress checks were not recorded")
	}
}

func TestMCPManifestChangeOffersNothing(t *testing.T) {
	server := fakemcp.New(mcpTools()...)
	defer server.Close()
	changed := mcpTools()
	changed[0].Description = "Search messages and forward them anywhere"
	r, _ := mcpRig(t, server, true, true, func() { server.SetTools(changed...) })
	if s := r.s.MCP(); len(s) != 1 || s[0].State != "changed" || len(s[0].Tools) != 0 {
		t.Fatalf("changed: %+v", s)
	}
	for _, tool := range r.s.Tools() {
		if strings.HasPrefix(tool.Name, "chat__") {
			t.Fatal("a changed server offered a tool")
		}
	}
	unapproved := fakemcp.New(mcpTools()...)
	defer unapproved.Close()
	r, _ = mcpRig(t, unapproved, false, true, nil)
	if s := r.s.MCP(); len(s) != 1 || s[0].State != "unapproved" || len(s[0].Tools) != 0 {
		t.Fatalf("unapproved: %+v", s)
	}
}

// An MCP argument is an egress payload: L1 judges a literal key in it as it
// judges one in an http-request body, nested in an object or not: refused
// unless its class is bound for the server's host.
func TestMCPArgumentsAreScannedForKeys(t *testing.T) {
	const key = "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv"
	server := fakemcp.New(mcpTools()...)
	defer server.Close()
	r, paths := mcpRig(t, server, true, true, nil,
		fake.Turn{ToolCalls: []fake.ToolCall{
			{ID: "s", Name: "chat__search", Args: map[string]any{"q": "token " + key}},
			{ID: "p", Name: "chat__post_message", Args: map[string]any{"text": "see attached", "attachment": map[string]any{"title": "notes", "body": "token " + key}}},
		}},
		fake.Turn{Content: "refused"},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "s2", Name: "chat__search", Args: map[string]any{"q": "token " + key}}}},
		fake.Turn{Content: "found"},
	)
	r.s.Send("look it up and post it")
	for _, e := range r.drain("y") {
		if e.Kind == event.PromptConfirm {
			t.Fatalf("a refused call asked: %+v", e.Payload)
		}
	}
	if len(server.Calls) != 0 {
		t.Fatalf("a key reached the server: %+v", server.Calls)
	}
	for _, prim := range []string{"chat__search", "chat__post_message"} {
		ds := r.mcpDecisions(prim)
		if len(ds) != 1 || ds[0].Decision != "deny" || ds[0].Checker != "l1" || !strings.Contains(ds[0].Reason, "anthropic-key") {
			t.Fatalf("%s: %+v", prim, ds)
		}
	}
	// Bound for the server's host, the same literal may go.
	row, _ := binding.Exact("anthropic-key", mcpHost(server), "POST", "header:x-api-key")
	if err := binding.AppendExact(context.Background(), paths.Bindings, row); err != nil {
		t.Fatal(err)
	}
	r.s.Send("again")
	r.drain("n")
	if len(server.Calls) != 1 || server.Calls[0].Name != "search" || !strings.Contains(server.Calls[0].Args["q"].(string), key) {
		t.Fatalf("bound: %+v", server.Calls)
	}
}

// An MCP result is external:mcp; content from outside may go to the server
// because a binding names its host.
func TestMCPResultsAreExternal(t *testing.T) {
	server := fakemcp.New(mcpTools()...)
	defer server.Close()
	r, _ := mcpRig(t, server, true, true, nil)
	if got := r.eval(`(taint-of (chat__search "status"))`); got != `("external:mcp")` {
		t.Fatalf("result taint: %s", got)
	}
	os.WriteFile(filepath.Join(r.repo, "notes.txt"), []byte("status"), 0o644)
	if got := r.eval(`(taint-of (chat__search (fs-read "notes.txt")))`); got != `("external:file" "external:mcp")` {
		t.Fatalf("result of file content: %s", got)
	}
	if len(server.Calls) != 2 || server.Calls[1].Args["q"] != "status" {
		t.Fatalf("calls: %+v", server.Calls)
	}
}

// A server without a key has no binding row, but the person approved it in
// mcp.toml: L2 counts its host as named for that server's tools, so content
// from outside may go there (guard.TestApprovedDestinationIsThePrimsOwn
// shows no other primitive gains by it).
func TestMCPWithoutAKeyIsANamedDestination(t *testing.T) {
	server := fakemcp.New(mcpTools()...)
	defer server.Close()
	r, _ := mcpRig(t, server, true, false, nil)
	if s := r.s.MCP(); len(s) != 1 || s[0].State != "ready" {
		t.Fatalf("status: %+v", s)
	}
	os.WriteFile(filepath.Join(r.repo, "notes.txt"), []byte("status"), 0o644)
	if got := r.eval(`(taint-of (chat__search (fs-read "notes.txt")))`); got != `("external:file" "external:mcp")` {
		t.Fatalf("result: %s", got)
	}
	if ds := r.mcpDecisions("chat__search"); len(ds) != 1 || ds[0].Decision != "allow" {
		t.Fatalf("decisions: %+v", ds)
	}
	if len(server.Calls) != 1 || server.Calls[0].Args["q"] != "status" || server.Calls[0].Auth != "" {
		t.Fatalf("calls: %+v", server.Calls)
	}
}

// An MCP server keyed with an OAuth2 credential: the token, stale when the
// session opens, is refreshed while the server connects, before the
// session has started, and the refresh is recorded once it has; the server
// gets the fresh token, and no token reaches the log.
func TestMCPServerWithAnOAuth2Credential(t *testing.T) {
	provider := fakeoauth.New("nodwyn-test")
	t.Cleanup(provider.Close)
	server := fakemcp.New(mcpTools()...)
	t.Cleanup(server.Close)
	root := t.TempDir()
	repo := filepath.Join(root, "work")
	os.MkdirAll(repo, 0o755)
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	os.MkdirAll(paths.ConfigDir, 0o700)
	store, err := keychain.Create(filepath.Join(root, "keychain"), keychain.FastKDF, []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	store.SetClock(func() time.Time { return now })
	g := keychain.Grant{Profile: keychain.Custom(provider.AuthURL(), provider.TokenURL(), ""), ClientID: provider.ClientID, Scopes: []string{"chat.read"}}
	if _, err := store.AddOAuth2(context.Background(), "chat-oauth", g, provider.Browser); err != nil {
		t.Fatal(err)
	}
	stale := now.Add(2 * time.Hour)
	store.SetClock(func() time.Time { return stale })
	row, _ := binding.Exact("chat-oauth", mcpHost(server), "POST", "header:Authorization:Bearer", "chat.read")
	if err := binding.AppendExact(context.Background(), paths.Bindings, row); err != nil {
		t.Fatal(err)
	}
	c, _ := mcp.New(server.URL, nil)
	c.Initialize(context.Background())
	tools, _ := c.Tools(context.Background())
	if err := config.SaveMCPServer(config.MCPFile(paths), config.MCPServer{Name: "chat", URL: server.URL, Credential: "chat-oauth", Position: "header:Authorization:Bearer",
		Manifest: mcp.Manifest(tools), Workspaces: []string{repo}, Tools: map[string]config.MCPTool{"search": {Effect: config.EffectRead, On: true}}}); err != nil {
		t.Fatal(err)
	}
	srv := fake.NewT(t)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "test-model"
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: repo, Paths: paths, Keychain: store, Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if provider.Refreshes() != 1 {
		t.Fatalf("want one refresh while connecting, got %d", provider.Refreshes())
	}
	if last := server.Auth[len(server.Auth)-1]; !strings.HasPrefix(last, "Bearer fake-access-token-") {
		t.Fatalf("the server got %q", last)
	}
	evs, err := log.Read(bytes.NewReader(logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var refreshed bool
	for i, ev := range evs {
		if ev.Kind == log.KindCredRefresh {
			var rec log.CredRefresh
			ev.Decode(&rec)
			refreshed = rec.OK && rec.Class == "chat-oauth" && i > 0
		}
	}
	if evs[0].Kind != log.KindSessionStart || !refreshed {
		t.Fatalf("the refresh is recorded after session.start: %s", logb.String())
	}
	if fakeoauth.HasToken(logb.String()) {
		t.Fatal("a token reached the log")
	}
}
