package config

import (
	"path/filepath"
	"testing"
)

// Any address of this machine is this machine, as the keychain says: the
// configuration check of an MCP server's address refused 127.0.0.2 while the
// keychain would have sent a key there (P2-056).
func TestMCPServerOnAnyLoopbackAddressMayUseHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.toml")
	for _, url := range []string{"http://127.0.0.2:9000/mcp", "http://[::1]:9000/mcp", "http://localhost:9000/mcp"} {
		if err := SaveMCPServer(path, MCPServer{Name: "local", URL: url}); err != nil {
			t.Errorf("refused %s: %v", url, err)
		}
	}
	if err := SaveMCPServer(path, MCPServer{Name: "remote", URL: "http://192.0.2.1/mcp"}); err == nil {
		t.Error("accepted plain http to another machine")
	}
}
