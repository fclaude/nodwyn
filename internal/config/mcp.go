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
	"github.com/fclaude/nodwyn/internal/egress"
)

// MCP servers (ADR 22 §4, ADR 23): mcp.toml in the config directory lists
// servers, remote or contained, what each tool does to the world as the
// person declared it, which tools are on, the manifest the person approved,
// and the workspaces where the server is offered. Nothing is on until
// chosen.

// Tool effects. A tool without one is undeclared: nodwyn treats it as sending
// out and irreversible, and asks every call.
const (
	EffectRead  = "read"  // reads from the server; still sends its arguments there
	EffectWrite = "write" // changes something at the server or beyond
)

// Transports. A server is remote unless it says otherwise.
const (
	TransportHTTP      = "http"      // a remote server over streamable HTTP
	TransportContainer = "container" // a local server nodwyn runs in a container (ADR 23 W3b)
	// transportHostProcess is a third-party server run on this computer
	// with the person's privileges: refused (ADR 23 §1).
	transportHostProcess = "host-process"
)

// MCPTool is the person's declaration about one tool.
type MCPTool struct {
	Effect string `toml:"effect,omitempty" json:"effect"`
	On     bool   `toml:"on" json:"on"`
}

// MCPServer is one server: remote (URL), or contained (Image and the
// isolation it runs with).
type MCPServer struct {
	Name      string `toml:"name" json:"name"`
	Transport string `toml:"transport,omitempty" json:"transport,omitempty"` // "" or "http" (remote), "container"
	URL       string `toml:"url,omitempty" json:"url"`
	// Image is a contained server's image, pinned by digest
	// (name@sha256:…): updating it is a deliberate edit.
	Image string `toml:"image,omitempty" json:"image,omitempty"`
	// Command and Args replace the image's entrypoint and its arguments,
	// as an argument list: no shell. Empty keeps the image's own.
	Command string   `toml:"command,omitempty" json:"command,omitempty"`
	Args    []string `toml:"args,omitempty" json:"args,omitempty"`
	// Network lists the hosts a contained server may reach, through
	// nodwyn's egress proxy (host for ports 443 and 80, or host:port).
	// Empty: no network at all.
	Network []string `toml:"network,omitempty" json:"network,omitempty"`
	// Mounts are workspace folders the server sees under /workspace:
	// "path" or "path:read" read-only, "path:write" writable. Empty: none.
	Mounts     []string           `toml:"mounts,omitempty" json:"mounts,omitempty"`
	Credential string             `toml:"credential,omitempty" json:"credential,omitempty"`
	Position   string             `toml:"position,omitempty" json:"position,omitempty"`
	Manifest   string             `toml:"manifest,omitempty" json:"manifest,omitempty"` // approved manifest hash
	Workspaces []string           `toml:"workspaces,omitempty" json:"workspaces"`
	Tools      map[string]MCPTool `toml:"tool,omitempty" json:"tools"`
}

// Host is a remote server's hostname; "" for a contained one.
func (s MCPServer) Host() string {
	if s.Contained() {
		return ""
	}
	u, _ := url.Parse(s.URL)
	return strings.ToLower(u.Hostname())
}

// Contained reports whether nodwyn runs the server in a container.
func (s MCPServer) Contained() bool { return s.Transport == TransportContainer }

// NetworkHosts are the hosts of a contained server's network list,
// lower-case, each once: where what it is sent may go next.
func (s MCPServer) NetworkHosts() []string {
	var hosts []string
	for _, entry := range s.Network {
		host, _, err := egress.SplitEntry(entry)
		if err == nil && !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// MCPMount is one workspace folder a contained server sees.
type MCPMount struct {
	Path  string // workspace-relative, cleaned
	Write bool
}

// MountList parses Mounts (Validate guarantees they parse).
func (s MCPServer) MountList() []MCPMount {
	out := make([]MCPMount, 0, len(s.Mounts))
	for _, m := range s.Mounts {
		if mount, err := parseMount(m); err == nil {
			out = append(out, mount)
		}
	}
	return out
}

// parseMount reads "path", "path:read" or "path:write": a folder inside
// the workspace, never the workspace itself (it holds .nodwyn/), never
// .nodwyn/ or anything under it, never outside.
func parseMount(m string) (MCPMount, error) {
	path, mode := m, "read"
	if i := strings.LastIndex(m, ":"); i >= 0 {
		path, mode = m[:i], m[i+1:]
	}
	if mode != "read" && mode != "write" {
		return MCPMount{}, fmt.Errorf("mount %q: say path, path:read or path:write", m)
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	first := strings.Split(filepath.ToSlash(clean), "/")[0]
	switch {
	case path == "" || strings.Contains(path, ":") || filepath.IsAbs(path) || filepath.VolumeName(path) != "":
		return MCPMount{}, fmt.Errorf("mount %q: a folder inside the workspace, by its relative path", m)
	case clean == "." || first == "..":
		return MCPMount{}, fmt.Errorf("mount %q: must be a folder inside the workspace, not the workspace or beyond it", m)
	case first == ".nodwyn":
		return MCPMount{}, fmt.Errorf("mount %q: .nodwyn/ holds the workspace's policy and is never mounted", m)
	}
	return MCPMount{Path: clean, Write: mode == "write"}, nil
}

// EnvName is the variable of an env:NAME position, or "".
func EnvName(position string) string {
	name, _ := strings.CutPrefix(position, "env:")
	if name == position {
		return ""
	}
	return name
}

var envVar = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// reservedEnv are variables nodwyn sets in a contained server itself, or
// that decide what runs: a credential may not take their place.
var reservedEnv = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "NODE_USE_ENV_PROXY", "PATH", "HOME", "LD_PRELOAD", "LD_LIBRARY_PATH"}

// imageRef is a digest-pinned image reference without a tag: an optional
// registry (a host, perhaps with :port), a path, then @sha256: and 64 hex
// characters.
var imageRef = regexp.MustCompile(`^(?:[a-z0-9]+(?:[.-][a-z0-9]+)*(?::[0-9]{1,5})?/)?[a-z0-9]+(?:[._-]+[a-z0-9]+)*(?:/[a-z0-9]+(?:[._-]+[a-z0-9]+)*)*@sha256:[0-9a-f]{64}$`)

// In reports whether the server is offered in the workspace at root.
func (s MCPServer) In(root string) bool {
	return slices.ContainsFunc(s.Workspaces, func(path string) bool { return CanonicalPath(path) == CanonicalPath(root) })
}

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
	switch s.Transport {
	case "", TransportHTTP:
		if err := s.validateRemote(); err != nil {
			return err
		}
	case TransportContainer:
		if err := s.validateContained(); err != nil {
			return err
		}
	case transportHostProcess:
		return fmt.Errorf("mcp: server %s: transport host-process would run a third-party server on this computer with your privileges, which nodwyn does not do (ADR 23 §1): use transport = \"container\"", s.Name)
	default:
		return fmt.Errorf("mcp: server %s: transport must be http or container, not %q", s.Name, s.Transport)
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

func (s MCPServer) validateRemote() error {
	u, err := url.Parse(s.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopbackHost(u.Hostname()))) {
		return fmt.Errorf("mcp: server %s: url must be https (http only on this computer)", s.Name)
	}
	if s.Image != "" || s.Command != "" || len(s.Args) > 0 || len(s.Network) > 0 || len(s.Mounts) > 0 {
		return fmt.Errorf("mcp: server %s: image, command, args, network and mounts are for transport = \"container\"", s.Name)
	}
	if s.Credential != "" && s.Position == "" {
		return fmt.Errorf("mcp: server %s: a credential needs a position, such as header:Authorization:Bearer", s.Name)
	}
	if EnvName(s.Position) != "" {
		return fmt.Errorf("mcp: server %s: an env: position is for a contained server; a remote one takes its key in a header or the query", s.Name)
	}
	return nil
}

// validateContained checks a contained server's isolation policy: an image
// pinned by digest, network entries, workspace mounts, and a credential
// only where it has somewhere to go.
func (s MCPServer) validateContained() error {
	if s.URL != "" {
		return fmt.Errorf("mcp: server %s: a contained server has no url: nodwyn talks to it over its standard input and output", s.Name)
	}
	if !strings.Contains(s.Image, "@sha256:") {
		return fmt.Errorf("mcp: server %s: image must be pinned by digest (name@sha256:…), not a tag: a tag can name different code tomorrow", s.Name)
	}
	if !imageRef.MatchString(s.Image) {
		return fmt.Errorf("mcp: server %s: image %q is not name@sha256: and 64 hex characters (a tag beside the digest is refused too)", s.Name, s.Image)
	}
	if strings.HasPrefix(s.Command, "[") || strings.ContainsRune(s.Command, 0) {
		return fmt.Errorf("mcp: server %s: command is one executable; its arguments go in args", s.Name)
	}
	for _, a := range s.Args {
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("mcp: server %s: an argument contains a NUL byte", s.Name)
		}
	}
	for _, entry := range s.Network {
		if _, _, err := egress.SplitEntry(entry); err != nil {
			return fmt.Errorf("mcp: server %s: network entry %q: a host name or address, or host:port, with no wildcard", s.Name, entry)
		}
	}
	for _, m := range s.Mounts {
		if _, err := parseMount(m); err != nil {
			return fmt.Errorf("mcp: server %s: %w", s.Name, err)
		}
	}
	if s.Credential == "" {
		if s.Position != "" {
			return fmt.Errorf("mcp: server %s: a position without a credential", s.Name)
		}
		return nil
	}
	if len(s.Network) == 0 {
		return fmt.Errorf("mcp: server %s: with network = [] the server reaches nothing, so a credential has nowhere to go: list the hosts it is for, or remove it", s.Name)
	}
	name := EnvName(s.Position)
	if !envVar.MatchString(name) {
		return fmt.Errorf("mcp: server %s: a contained server's credential goes in an environment variable: position = \"env:NAME\"", s.Name)
	}
	if slices.Contains(reservedEnv, strings.ToUpper(name)) {
		return fmt.Errorf("mcp: server %s: %s is not for a credential; name another variable", s.Name, name)
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
	b.WriteString("# MCP servers nodwyn may use (ADR 22, 23). A tool without an effect is treated as\n")
	b.WriteString("# sending out and irreversible. manifest is the tool list you approved. A contained\n")
	b.WriteString("# server reaches only its network hosts, through nodwyn's proxy; [] is none.\n")
	for _, s := range list {
		fmt.Fprintf(&b, "\n[[server]]\nname = %s\n", quote(s.Name))
		if s.Contained() {
			fmt.Fprintf(&b, "transport = %s\nimage = %s\n", quote(s.Transport), quote(s.Image))
			if s.Command != "" {
				fmt.Fprintf(&b, "command = %s\n", quote(s.Command))
			}
			if len(s.Args) > 0 {
				fmt.Fprintf(&b, "args = %s\n", quoteList(s.Args))
			}
			fmt.Fprintf(&b, "network = %s\nmounts = %s\n", quoteList(s.Network), quoteList(s.Mounts))
		} else {
			fmt.Fprintf(&b, "url = %s\n", quote(s.URL))
		}
		if s.Credential != "" {
			fmt.Fprintf(&b, "credential = %s\nposition = %s\n", quote(s.Credential), quote(s.Position))
		}
		if s.Manifest != "" {
			fmt.Fprintf(&b, "manifest = %s\n", quote(s.Manifest))
		}
		if len(s.Workspaces) > 0 {
			fmt.Fprintf(&b, "workspaces = %s\n", quoteList(s.Workspaces))
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

func quoteList(list []string) string {
	quoted := make([]string, len(list))
	for i, v := range list {
		quoted[i] = quote(v)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
