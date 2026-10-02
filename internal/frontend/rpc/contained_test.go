package rpc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/container"
	fakecontainer "github.com/fclaude/nodwyn/test/fakes/container"
	fakemcp "github.com/fclaude/nodwyn/test/fakes/mcp"
)

// The test binary is also the fake container runtime.
func TestMain(m *testing.M) {
	fakecontainer.Main(fakemcp.Tool{Name: "search", Description: "Search the notes", Reply: func(map[string]any) (string, bool) { return "ok", false }})
	os.Exit(m.Run())
}

const notesImage = "ghcr.io/example/mcp-notes@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// Connections adds a contained server with a key, which is bound for every
// host on its network list at its variable; says the image is missing;
// pulls it on request; reviews and approves its tools; and a task offers
// them.
func TestContainedServerFromConnections(t *testing.T) {
	fake := fakecontainer.New(t, "podman")
	o := options(t)
	o.Containers = &container.Runtime{Kind: "podman", Path: fake.Path}
	o.Config.MCP.Egress = filepath.Join(t.TempDir(), "nodwyn-egress")
	os.WriteFile(o.Config.MCP.Egress, []byte("#!/bin/false\n"), 0o755)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	repo := t.TempDir()
	c.ok("keychain.create", map[string]any{"passphrase": "correct horse"})
	entry := map[string]any{"name": "notes", "transport": "container", "image": notesImage, "network": []string{"api.notes.example", "files.notes.example:8443"},
		"position": "env:NOTES_TOKEN", "workspaces": []string{repo}, "tools": map[string]any{"search": map[string]any{"effect": "read", "on": true}}}
	var saved config.MCPServer
	json.Unmarshal(c.ok("mcp.save", map[string]any{"server": entry, "api_key": "sk-notes-canary-000000"}), &saved)
	if saved.Credential != "notes-key" || saved.Position != "env:NOTES_TOKEN" || !saved.Contained() {
		t.Fatalf("saved: %+v", saved)
	}
	table, err := binding.Load(o.Paths.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"api.notes.example", "files.notes.example"} {
		if bound, reason := table.Lookup("notes-key", host, "", "env:NOTES_TOKEN"); !bound {
			t.Fatalf("%s: %s", host, reason)
		}
	}
	var state mcpRuntime
	json.Unmarshal(c.ok("mcp.runtime", map[string]any{}), &state)
	if state.Runtime != "podman" || state.Egress != "" || state.Images["notes"] != "missing" {
		t.Fatalf("runtime: %+v", state)
	}
	if r := c.call("mcp.inspect", map[string]any{"name": "notes"}); r.Error == nil || !strings.Contains(r.Error.Message, "nodwyn mcp pull notes") {
		t.Fatalf("inspected without the image: %+v", r.Error)
	}
	var pulled mcpPulled
	json.Unmarshal(c.ok("mcp.pull", map[string]any{"name": "notes"}), &pulled)
	if !pulled.Present || pulled.Image != notesImage || len(fake.Find("pull", notesImage)) != 1 {
		t.Fatalf("pull: %+v", pulled)
	}
	json.Unmarshal(c.ok("mcp.runtime", map[string]any{}), &state)
	if state.Images["notes"] != "present" {
		t.Fatalf("after pull: %+v", state)
	}
	var inspection mcpInspection
	json.Unmarshal(c.ok("mcp.inspect", map[string]any{"name": "notes"}), &inspection)
	if len(inspection.Tools) != 1 || inspection.Manifest == "" {
		t.Fatalf("inspection: %+v", inspection)
	}
	var runs []fakecontainer.Invocation // of the server, not its proxy
	for _, inv := range fake.Find("run", "-i") {
		if !slices.Contains(inv.Args, "--entrypoint=/nodwyn-egress") {
			runs = append(runs, inv)
		}
	}
	if len(runs) != 1 || !strings.Contains(strings.Join(runs[0].Env, "\n"), "NOTES_TOKEN=sk-notes-canary-000000") {
		t.Fatalf("the review did not run with its bound key: %+v", runs)
	}
	c.ok("mcp.approve", map[string]any{"name": "notes", "manifest": inspection.Manifest})
	// Another image is another program: saving one drops the approval.
	other := map[string]any{}
	for k, v := range entry {
		other[k] = v
	}
	other["image"] = strings.Replace(notesImage, "0123", "4567", 1)
	json.Unmarshal(c.ok("mcp.save", map[string]any{"server": other}), &saved)
	if saved.Manifest != "" || saved.Credential != "notes-key" {
		t.Fatalf("a new image kept the approval: %+v", saved)
	}
	json.Unmarshal(c.ok("mcp.save", map[string]any{"server": entry}), &saved)
	c.ok("mcp.approve", map[string]any{"name": "notes", "manifest": inspection.Manifest})
	var details sessionDetails
	json.Unmarshal(c.ok("session.open", map[string]any{"repo": repo}), &details)
	if len(details.MCP) != 1 || details.MCP[0].State != "ready" || details.MCP[0].Transport != "container" || strings.Join(details.MCP[0].Tools, ",") != "notes__search" {
		t.Fatalf("session mcp: %+v", details.MCP)
	}
	if r := c.call("mcp.pull", map[string]any{"name": "nobody"}); r.Error == nil {
		t.Fatal("pulled for a server that is not there")
	}
	raw, _ := os.ReadFile(config.MCPFile(o.Paths))
	if strings.Contains(string(raw), "canary") {
		t.Fatal("the key was written into mcp.toml")
	}
}
