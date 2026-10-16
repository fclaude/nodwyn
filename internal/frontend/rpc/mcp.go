package rpc

import (
	"context"
	"io"
	"net/url"
	"slices"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/mcp"
)

// MCP servers for the Connections page (ADR 22 §4, ADR 23). The list is
// the configuration; inspecting connects without a session to show the
// live tools and whether they match what was approved (a contained server
// is started for it and removed after). mcp.runtime says whether contained
// servers can run here and whose image is present; mcp.pull downloads one,
// by digest, which nothing else does.

type mcpNameParams struct {
	Name string `json:"name"`
}
type mcpSaveParams struct {
	Server config.MCPServer `json:"server"`
	// APIKey, when set, is stored in the keychain under the server's
	// credential class with binding rows: for a remote server one, its
	// host, POST, its position; for a contained one, one per host on its
	// network list, at its env: position (any method: the server makes
	// the requests).
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

// mcpRuntime is whether contained servers can run here.
type mcpRuntime struct {
	// Runtime is "podman" or "docker", or "" when there is none (Detail
	// says why); remote servers work either way.
	Runtime string `json:"runtime"`
	Detail  string `json:"detail,omitempty"`
	// Egress is "" when nodwyn-egress was found, else why a server with a
	// network list cannot run.
	Egress string `json:"egress,omitempty"`
	// Images is, per contained server, "present", "missing", or why the
	// runtime could not say.
	Images map[string]string `json:"images"`
}

type mcpPulled struct {
	Image   string `json:"image"`
	Present bool   `json:"present"`
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
	case "mcp.runtime":
		if err := params(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		list, err := config.LoadMCP(path)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		return s.containerState(list), nil
	case "mcp.pull":
		var p mcpNameParams
		if err := params(req.Params, &p, "name"); err != nil {
			return nil, err
		}
		server, rerr := s.mcpFind(p.Name)
		if rerr != nil {
			return nil, rerr
		}
		if !server.Contained() {
			return nil, failure(-32602, server.Name+" is a remote server: nothing to pull")
		}
		present, err := s.pull(server)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		return mcpPulled{Image: server.Image, Present: present}, nil
	}
	var p mcpSaveParams
	if err := params(req.Params, &p, "server", "api_key"); err != nil {
		return nil, err
	}
	key := []byte(p.APIKey)
	defer clear(key)
	server := p.Server
	// Approval is mcp.approve's alone, after a live re-read: a save keeps
	// the approved manifest only for the same program, the same address or
	// the same image, entrypoint and arguments (other code is another
	// program, whatever tools it lists). The credential stays with a remote
	// server's address, and with a contained server whatever its image:
	// its bindings name the hosts.
	server.Manifest = ""
	if old, rerr := s.mcpFind(server.Name); rerr == nil {
		if sameProgram(old, server) {
			server.Manifest = old.Manifest
		}
		keep := old.Contained() == server.Contained() && (server.Contained() || old.URL == server.URL)
		if keep && server.Credential == "" && len(key) == 0 {
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
		var rows []binding.Binding
		server.Credential = s.freeClass(server.Name + "-key")
		if server.Contained() {
			if config.EnvName(server.Position) == "" {
				return nil, failure(-32602, "name the environment variable the server reads its key from, such as NOTION_TOKEN")
			}
			for _, host := range server.NetworkHosts() {
				row, err := binding.Exact(server.Credential, host, "*", server.Position)
				if err != nil {
					return nil, failure(-32602, err.Error())
				}
				rows = append(rows, row)
			}
		} else {
			if server.Position == "" {
				server.Position = "header:Authorization:Bearer"
			}
			u, err := url.Parse(server.URL)
			if err != nil || u.Hostname() == "" {
				return nil, failure(-32602, "url must be an https address")
			}
			row, err := binding.Exact(server.Credential, u.Hostname(), "POST", server.Position)
			if err != nil {
				return nil, failure(-32602, err.Error())
			}
			rows = append(rows, row)
		}
		// Validate the whole entry before storing anything.
		if err := server.Validate(); err != nil {
			return nil, failure(-32602, err.Error())
		}
		if _, err := s.options.Keychain.Add(server.Credential, key); err != nil {
			return nil, failure(-32000, err.Error())
		}
		for _, row := range rows {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := binding.AppendExact(ctx, s.options.Paths.Bindings, row)
			cancel()
			if err != nil {
				return nil, failure(-32000, err.Error())
			}
		}
	}
	if err := config.SaveMCPServer(path, server); err != nil {
		return nil, failure(-32602, err.Error())
	}
	return server, nil
}

// sameProgram reports whether two entries of a server name the same thing
// to talk to: the address of a remote one, the image, entrypoint and
// arguments of a contained one.
func sameProgram(a, b config.MCPServer) bool {
	if a.Contained() != b.Contained() {
		return false
	}
	if !a.Contained() {
		return a.URL == b.URL
	}
	return a.Image == b.Image && a.Command == b.Command && slices.Equal(a.Args, b.Args)
}

// inspect runs without the lifecycle lock: a slow server must not hold
// every other method.
func (s *server) inspect(server config.MCPServer) ([]mcp.Tool, string, error) {
	options := s.options
	s.mu.Unlock()
	defer s.mu.Lock()
	return core.InspectMCP(options, server)
}

// containerState asks the runtime about each contained server's image,
// without the lifecycle lock.
func (s *server) containerState(list []config.MCPServer) mcpRuntime {
	options := s.options
	s.mu.Unlock()
	defer s.mu.Lock()
	state := mcpRuntime{Images: map[string]string{}}
	if _, err := core.EgressBinary(options.Config.MCP); err != nil {
		state.Egress = err.Error()
	}
	rt, err := core.ContainerRuntime(options)
	if err != nil {
		state.Detail = err.Error()
		return state
	}
	state.Runtime = rt.Kind
	for _, server := range list {
		if !server.Contained() {
			continue
		}
		ctx, cancel := context.WithTimeout(s.base(), 20*time.Second)
		present, err := rt.ImagePresent(ctx, server.Image)
		cancel()
		switch {
		case err != nil:
			state.Images[server.Name] = err.Error()
		case present:
			state.Images[server.Name] = "present"
		default:
			state.Images[server.Name] = "missing"
		}
	}
	return state
}

// pull downloads a contained server's image by its digest, without the
// lifecycle lock: it can take minutes.
func (s *server) pull(server config.MCPServer) (bool, error) {
	options := s.options
	s.mu.Unlock()
	defer s.mu.Lock()
	rt, err := core.ContainerRuntime(options)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(s.base(), 30*time.Minute)
	defer cancel()
	if err := rt.Pull(ctx, server.Image, io.Discard); err != nil {
		return false, err
	}
	return rt.ImagePresent(ctx, server.Image)
}
