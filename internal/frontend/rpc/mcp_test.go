package rpc

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/config"
	fakemcp "github.com/fclaude/nibb/test/fakes/mcp"
)

func TestMCPReviewApproveAndOffer(t *testing.T) {
	server := fakemcp.New(fakemcp.Tool{Name: "search", Description: "Search", Reply: func(map[string]any) (string, bool) { return "ok", false }})
	defer server.Close()
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	repo := t.TempDir()
	entry := map[string]any{"name": "chat", "url": server.URL, "workspaces": []string{repo}, "tools": map[string]any{"search": map[string]any{"effect": "read", "on": true}}}
	if r := c.call("mcp.save", map[string]any{"server": entry, "api_key": "sk-mcp-canary-0000000000"}); r.Error == nil {
		t.Fatal("stored a key without a keychain")
	}
	c.ok("keychain.create", map[string]any{"passphrase": "correct horse"})
	var saved config.MCPServer
	json.Unmarshal(c.ok("mcp.save", map[string]any{"server": entry, "api_key": "sk-mcp-canary-0000000000"}), &saved)
	if saved.Credential != "chat-key" || saved.Position != "header:Authorization:Bearer" || saved.Manifest != "" {
		t.Fatalf("saved: %+v", saved)
	}
	raw, _ := os.ReadFile(config.MCPFile(o.Paths))
	if strings.Contains(string(raw), "canary") {
		t.Fatal("key written into mcp.toml")
	}
	var inspection mcpInspection
	json.Unmarshal(c.ok("mcp.inspect", map[string]any{"name": "chat"}), &inspection)
	if len(inspection.Tools) != 1 || inspection.Manifest == "" || inspection.Changed {
		t.Fatalf("inspection: %+v", inspection)
	}
	if server.Auth[len(server.Auth)-1] != "Bearer sk-mcp-canary-0000000000" {
		t.Fatalf("inspection did not inject the bound key: %q", server.Auth)
	}
	if r := c.call("mcp.approve", map[string]any{"name": "chat", "manifest": "stale"}); r.Error == nil {
		t.Fatal("approved a manifest that is not live")
	}
	c.ok("mcp.approve", map[string]any{"name": "chat", "manifest": inspection.Manifest})
	var details sessionDetails
	json.Unmarshal(c.ok("session.open", map[string]any{"repo": repo}), &details)
	if len(details.MCP) != 1 || details.MCP[0].State != "ready" || strings.Join(details.MCP[0].Tools, ",") != "chat__search" {
		t.Fatalf("session mcp: %+v", details.MCP)
	}
	found := false
	for _, tool := range details.Tools {
		found = found || tool.Name == "chat__search"
	}
	if !found {
		t.Fatal("the tool is not offered")
	}
	c.ok("mcp.remove", map[string]any{"name": "chat"})
	var list []config.MCPServer
	json.Unmarshal(c.ok("mcp.list", map[string]any{}), &list)
	if len(list) != 0 {
		t.Fatalf("after remove: %+v", list)
	}
}
