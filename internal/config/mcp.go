package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// MCP servers (ADR 22 §4, ADR 23): mcp.toml in the config directory lists
// remote servers, what each tool does to the world as the person declared
// it, which tools are on, the manifest the person approved, and the
// workspaces where the server is offered. Nothing is on until chosen.

// Tool effects. A tool without one is undeclared: nibb treats it as sending
// out and irreversible, and asks every call.
const (
	EffectRead  = "read"  // reads from the server; still sends its arguments there
	EffectWrite = "write" // changes something at the server or beyond
)

// MCPTool is the person's declaration about one tool.
type MCPTool struct {
	Effect string `toml:"effect,omitempty" json:"effect"`
	On     bool   `toml:"on" json:"on"`
}

// MCPServer is one remote server.
type MCPServer struct {
	Name       string             `toml:"name" json:"name"`
	URL        string             `toml:"url" json:"url"`
	Credential string             `toml:"credential,omitempty" json:"credential,omitempty"`
	Position   string             `toml:"position,omitempty" json:"position,omitempty"`
	Manifest   string             `toml:"manifest,omitempty" json:"manifest,omitempty"` // approved manifest hash
	Workspaces []string           `toml:"workspaces,omitempty" json:"workspaces"`
	Tools      map[string]MCPTool `toml:"tool,omitempty" json:"tools"`
}

// Host is the server's hostname.
func (s MCPServer) Host() string {
	u, _ := url.Parse(s.URL)
	return strings.ToLower(u.Hostname())
}

// In reports whether the server is offered in the workspace at root.
func (s MCPServer) In(root string) bool { return slices.Contains(s.Workspaces, root) }

type mcpFile struct {
	Server []MCPServer `toml:"server"`
}

// MCPFile is where servers are configured.
func MCPFile(p Paths) string { return filepath.Join(p.ConfigDir, "mcp.toml") }

var serverName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ToolName is how an MCP tool is named to the model: server and tool joined
// by a double underscore (model APIs refuse slashes in tool names). The
// console shows server/tool.
func ToolName(server, tool string) string { return server + "__" + tool }

// LoadMCP reads the configured servers; missing is none.
func LoadMCP(path string) ([]MCPServer, error) {
	var f mcpFile
	md, err := toml.DecodeFile(path, &f)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return nil, fmt.Errorf("mcp: unknown keys: %v", undec)
	}
	seen := map[string]bool{}
	for i, s := range f.Server {
		if err := s.Validate(); err != nil {
			return nil, err
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("mcp: server %s is listed twice", s.Name)
		}
		seen[s.Name] = true
		if f.Server[i].Tools == nil {
			f.Server[i].Tools = map[string]MCPTool{}
		}
	}
	return f.Server, nil
}

// Validate checks a server entry as loading would.
func (s MCPServer) Validate() error {
	if !serverName.MatchString(s.Name) {
		return fmt.Errorf("mcp: server name %q: lower-case letters, digits and dashes", s.Name)
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopbackHost(u.Hostname()))) {
		return fmt.Errorf("mcp: server %s: url must be https (http only on this computer)", s.Name)
	}
	if s.Credential != "" && s.Position == "" {
		return fmt.Errorf("mcp: server %s: a credential needs a position, such as header:Authorization:Bearer", s.Name)
	}
	for name, t := range s.Tools {
		if t.Effect != "" && t.Effect != EffectRead && t.Effect != EffectWrite {
			return fmt.Errorf("mcp: %s/%s: effect must be read or write", s.Name, name)
		}
	}
	for _, w := range s.Workspaces {
		if !filepath.IsAbs(w) {
			return fmt.Errorf("mcp: server %s: workspace %s must be absolute", s.Name, w)
		}
	}
	return nil
}

func loopbackHost(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

// SaveMCPServer adds or replaces the server of the same name.
func SaveMCPServer(path string, server MCPServer) error {
	if err := server.Validate(); err != nil {
		return err
	}
	list, err := LoadMCP(path)
	if err != nil {
		return err
	}
	list = slices.DeleteFunc(list, func(s MCPServer) bool { return s.Name == server.Name })
	list = append(list, server)
	return writeMCP(path, list)
}

// RemoveMCPServer forgets a server.
func RemoveMCPServer(path, name string) error {
	list, err := LoadMCP(path)
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(slices.Clone(list), func(s MCPServer) bool { return s.Name == name })
	if len(kept) == len(list) {
		return fmt.Errorf("mcp: no server named %s", name)
	}
	return writeMCP(path, kept)
}

func writeMCP(path string, list []MCPServer) error {
	var b strings.Builder
	b.WriteString("# MCP servers nibb may use (ADR 22, 23). A tool without an effect is treated as\n")
	b.WriteString("# sending out and irreversible. manifest is the tool list you approved.\n")
	for _, s := range list {
		fmt.Fprintf(&b, "\n[[server]]\nname = %s\nurl = %s\n", quote(s.Name), quote(s.URL))
		if s.Credential != "" {
			fmt.Fprintf(&b, "credential = %s\nposition = %s\n", quote(s.Credential), quote(s.Position))
		}
		if s.Manifest != "" {
			fmt.Fprintf(&b, "manifest = %s\n", quote(s.Manifest))
		}
		if len(s.Workspaces) > 0 {
			quoted := make([]string, len(s.Workspaces))
			for i, w := range s.Workspaces {
				quoted[i] = quote(w)
			}
			fmt.Fprintf(&b, "workspaces = [%s]\n", strings.Join(quoted, ", "))
		}
		names := make([]string, 0, len(s.Tools))
		for name := range s.Tools {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			t := s.Tools[name]
			fmt.Fprintf(&b, "\n[server.tool.%s]\non = %v\n", quote(name), t.On)
			if t.Effect != "" {
				fmt.Fprintf(&b, "effect = %s\n", quote(t.Effect))
			}
		}
	}
	return WriteFile(path, []byte(b.String()))
}
