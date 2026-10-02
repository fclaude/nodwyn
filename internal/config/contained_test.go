package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pinnedImage = "ghcr.io/example/mcp-notes@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestContainedServerConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.toml")
	server := MCPServer{Name: "notes", Transport: TransportContainer, Image: pinnedImage, Command: "/usr/bin/notes", Args: []string{"--stdio"},
		Network: []string{"API.notes.example", "files.notes.example:8443", "api.notes.example"}, Mounts: []string{"docs", "out:write"},
		Credential: "notes-key", Position: "env:NOTES_TOKEN", Workspaces: []string{"/Users/x/Notes"}, Tools: map[string]MCPTool{"search": {Effect: EffectRead, On: true}}}
	if err := SaveMCPServer(path, server); err != nil {
		t.Fatal(err)
	}
	list, err := LoadMCP(path)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	got := list[0]
	if !got.Contained() || got.Host() != "" || got.Image != pinnedImage || got.Command != "/usr/bin/notes" || got.Args[0] != "--stdio" || got.Position != "env:NOTES_TOKEN" {
		t.Fatalf("%+v", got)
	}
	if hosts := got.NetworkHosts(); len(hosts) != 2 || hosts[0] != "api.notes.example" || hosts[1] != "files.notes.example" {
		t.Fatalf("hosts %v", hosts)
	}
	if m := got.MountList(); len(m) != 2 || m[0] != (MCPMount{Path: "docs"}) || m[1] != (MCPMount{Path: "out", Write: true}) {
		t.Fatalf("mounts %+v", m)
	}
	// network = [] is no network, and is written down as such.
	bare := MCPServer{Name: "offline", Transport: TransportContainer, Image: pinnedImage}
	if err := SaveMCPServer(path, bare); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "network = []\nmounts = []") {
		t.Fatalf("written:\n%s", raw)
	}
}

func TestContainedServerRefusals(t *testing.T) {
	ok := MCPServer{Name: "notes", Transport: TransportContainer, Image: pinnedImage}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*MCPServer){
		"a tag":    func(s *MCPServer) { s.Image = "ghcr.io/example/mcp-notes:latest" },
		"no image": func(s *MCPServer) { s.Image = "" },
		"a tag beside the digest": func(s *MCPServer) {
			s.Image = "ghcr.io/example/mcp-notes:1.2@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		},
		"a short digest":           func(s *MCPServer) { s.Image = "ghcr.io/example/mcp-notes@sha256:0123" },
		"a url":                    func(s *MCPServer) { s.URL = "https://mcp.example.com" },
		"a mount outside":          func(s *MCPServer) { s.Mounts = []string{"../elsewhere"} },
		"an absolute mount":        func(s *MCPServer) { s.Mounts = []string{"/etc"} },
		"the workspace itself":     func(s *MCPServer) { s.Mounts = []string{"."} },
		".nodwyn":                  func(s *MCPServer) { s.Mounts = []string{".nodwyn"} },
		"inside .nodwyn":           func(s *MCPServer) { s.Mounts = []string{"docs/../.nodwyn/x"} },
		"a bad mode":               func(s *MCPServer) { s.Mounts = []string{"docs:rw"} },
		"a wildcard host":          func(s *MCPServer) { s.Network = []string{"*.example.com"} },
		"a url as a host":          func(s *MCPServer) { s.Network = []string{"https://api.example.com"} },
		"a credential, no network": func(s *MCPServer) { s.Credential, s.Position = "notes-key", "env:NOTES_TOKEN" },
		"a header position": func(s *MCPServer) {
			s.Network, s.Credential, s.Position = []string{"api.example.com"}, "notes-key", "header:Authorization:Bearer"
		},
		"a reserved variable": func(s *MCPServer) {
			s.Network, s.Credential, s.Position = []string{"api.example.com"}, "notes-key", "env:HTTPS_PROXY"
		},
		"a position, no credential": func(s *MCPServer) { s.Position = "env:NOTES_TOKEN" },
		"a JSON command":            func(s *MCPServer) { s.Command = `["sh","-c","curl evil"]` },
		"host-process":              func(s *MCPServer) { s.Transport = "host-process" },
		"another transport":         func(s *MCPServer) { s.Transport = "stdio" },
	}
	for name, change := range cases {
		s := ok
		change(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s := ok
	s.Transport = "host-process"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "ADR 23") {
		t.Fatalf("host-process: %v", err)
	}
	s = ok
	s.Image = "ghcr.io/example/mcp-notes:latest"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "pinned by digest") {
		t.Fatalf("tag: %v", err)
	}
	// Contained fields on a remote server, or an env: position, are refused.
	for _, remote := range []MCPServer{
		{Name: "chat", URL: "https://mcp.example.com", Image: pinnedImage},
		{Name: "chat", URL: "https://mcp.example.com", Network: []string{"api.example.com"}},
		{Name: "chat", URL: "https://mcp.example.com", Credential: "k", Position: "env:TOKEN"},
	} {
		if err := remote.Validate(); err == nil {
			t.Errorf("remote accepted %+v", remote)
		}
	}
}

func TestMCPRuntimeConfig(t *testing.T) {
	if _, err := Parse([]byte("[mcp]\nruntime = \"docker\"\negress = \"/opt/nodwyn/nodwyn-egress\"\n")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"[mcp]\nruntime = \"lxc\"\n", "[mcp]\negress = \"bin/nodwyn-egress\"\n"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
