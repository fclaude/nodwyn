package rpc

import (
	"context"
	"net/url"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/mcp"
)

// MCP servers for the Connections page (ADR 22 §4, ADR 23). The list is
// the configuration; inspecting connects without a session to show the
// live tools and whether they match what was approved.

type mcpNameParams struct {
	Name string `json:"name"`
}
type mcpSaveParams struct {
	Server config.MCPServer `json:"server"`
	// APIKey, when set, is stored in the keychain under the server's
	// credential class with one binding row: its host, POST, its position.
	APIKey string `json:"api_key,omitempty"`
}
type mcpApproveParams struct {
	Name     string `json:"name"`
	Manifest string `json:"manifest"`
}
type mcpInspection struct {
	Tools    []mcp.Tool `json:"tools"`
	Manifest string     `json:"manifest"`
	Approved string     `json:"approved"`
	Changed  bool       `json:"changed"`
}

func (s *server) mcpFind(name string) (config.MCPServer, *rpcError) {
	list, err := config.LoadMCP(config.MCPFile(s.options.Paths))
	if err != nil {
		return config.MCPServer{}, failure(-32000, err.Error())
	}
	for _, server := range list {
		if server.Name == name {
			return server, nil
		}
	}
	return config.MCPServer{}, failure(-32602, "no server named "+name)
}

func (s *server) mcpMethod(req request, _ *peer) (any, *rpcError) {
	path := config.MCPFile(s.options.Paths)
	switch req.Method {
	case "mcp.list":
		if err := params(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		list, err := config.LoadMCP(path)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		if list == nil {
			list = []config.MCPServer{}
		}
		return list, nil
	case "mcp.inspect":
		var p mcpNameParams
		if err := params(req.Params, &p, "name"); err != nil {
			return nil, err
		}
		server, rerr := s.mcpFind(p.Name)
		if rerr != nil {
			return nil, rerr
		}
		tools, manifest, err := s.inspect(server)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		return mcpInspection{Tools: tools, Manifest: manifest, Approved: server.Manifest, Changed: server.Manifest != "" && server.Manifest != manifest}, nil
	case "mcp.approve":
		var p mcpApproveParams
		if err := params(req.Params, &p, "name", "manifest"); err != nil {
			return nil, err
		}
		server, rerr := s.mcpFind(p.Name)
		if rerr != nil {
			return nil, rerr
		}
		// Approve what the person reviewed, and only if it is still what
		// the server offers.
		_, manifest, err := s.inspect(server)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		if manifest != p.Manifest {
			return nil, failure(-32000, "the server's tools changed again since you reviewed them: review them once more")
		}
		server.Manifest = manifest
		if err := config.SaveMCPServer(path, server); err != nil {
			return nil, failure(-32000, err.Error())
		}
		return server, nil
	case "mcp.remove":
		var p mcpNameParams
		if err := params(req.Params, &p, "name"); err != nil {
			return nil, err
		}
		if err := config.RemoveMCPServer(path, p.Name); err != nil {
			return nil, failure(-32000, err.Error())
		}
		return struct{}{}, nil
	}
	var p mcpSaveParams
	if err := params(req.Params, &p, "server", "api_key"); err != nil {
		return nil, err
	}
	key := []byte(p.APIKey)
	defer clear(key)
	server := p.Server
	// Approval is mcp.approve's alone, after a live re-read: a save keeps
	// the approved manifest only for the same address.
	server.Manifest = ""
	if old, err := s.mcpFind(server.Name); err == nil && old.URL == server.URL {
		server.Manifest = old.Manifest
		if server.Credential == "" && len(key) == 0 {
			server.Credential, server.Position = old.Credential, old.Position
		}
	}
	if server.Tools == nil {
		server.Tools = map[string]config.MCPTool{}
	}
	if len(key) > 0 {
		if s.options.Keychain == nil {
			return nil, failure(-32000, "Unlock or create the keychain first: the key is kept there")
		}
		if server.Position == "" {
			server.Position = "header:Authorization:Bearer"
		}
		u, err := url.Parse(server.URL)
		if err != nil || u.Hostname() == "" {
			return nil, failure(-32602, "url must be an https address")
		}
		server.Credential = s.freeClass(server.Name + "-key")
		row, err := binding.Exact(server.Credential, u.Hostname(), "POST", server.Position)
		if err != nil {
			return nil, failure(-32602, err.Error())
		}
		// Validate the whole entry before storing anything.
		if err := server.Validate(); err != nil {
			return nil, failure(-32602, err.Error())
		}
		if _, err := s.options.Keychain.Add(server.Credential, key); err != nil {
			return nil, failure(-32000, err.Error())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = binding.AppendExact(ctx, s.options.Paths.Bindings, row)
		cancel()
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
	}
	if err := config.SaveMCPServer(path, server); err != nil {
		return nil, failure(-32602, err.Error())
	}
	return server, nil
}

// inspect runs without the lifecycle lock: a slow server must not hold
// every other method.
func (s *server) inspect(server config.MCPServer) ([]mcp.Tool, string, error) {
	options := s.options
	s.mu.Unlock()
	defer s.mu.Lock()
	return core.InspectMCP(options, server)
}
