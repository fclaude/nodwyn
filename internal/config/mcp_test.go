package config

import (
	"path/filepath"
	"testing"
)

func TestMCPConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.toml")
	server := MCPServer{Name: "linear", URL: "https://mcp.linear.app/mcp", Credential: "linear-key", Position: "header:Authorization:Bearer", Manifest: "abc", Workspaces: []string{"/Users/x/Notes"},
		Tools: map[string]MCPTool{"search_issues": {Effect: EffectRead, On: true}, "create_issue": {On: false}, "weird \"name\"": {On: true}}}
	if err := SaveMCPServer(path, server); err != nil {
		t.Fatal(err)
	}
	list, err := LoadMCP(path)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	got := list[0]
	if got.Host() != "mcp.linear.app" || !got.In("/Users/x/Notes") || got.Tools["search_issues"].Effect != EffectRead || !got.Tools["weird \"name\""].On || got.Tools["create_issue"].On {
		t.Fatalf("%+v", got)
	}
	for _, bad := range []MCPServer{
		{Name: "Bad Name", URL: "https://x"},
		{Name: "x", URL: "http://example.com/mcp"},
		{Name: "x", URL: "https://x", Credential: "k"},
		{Name: "x", URL: "https://x", Tools: map[string]MCPTool{"t": {Effect: "delete"}}},
		{Name: "x", URL: "https://x", Workspaces: []string{"relative"}},
	} {
		if err := SaveMCPServer(path, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if err := SaveMCPServer(path, MCPServer{Name: "local", URL: "http://127.0.0.1:9000/mcp"}); err != nil {
		t.Fatal("refused a loopback http server", err)
	}
	if err := RemoveMCPServer(path, "linear"); err != nil {
		t.Fatal(err)
	}
	list, _ = LoadMCP(path)
	if len(list) != 1 || list[0].Name != "local" {
		t.Fatal(list)
	}
}
