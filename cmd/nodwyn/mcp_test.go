package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	fakecontainer "github.com/fclaude/nodwyn/test/fakes/container"
)

// The test binary is also the fake container runtime.
func TestMain(m *testing.M) {
	fakecontainer.Main()
	os.Exit(m.Run())
}

// nodwyn mcp lists servers and pulls a contained server's image by its
// digest, with the runtime config.toml names; a remote server has nothing
// to pull.
func TestCLIMCPPull(t *testing.T) {
	fake := fakecontainer.New(t, "podman")
	dir := t.TempDir()
	paths := config.PathsUnder(filepath.Join(dir, "config", "nodwyn"), filepath.Join(dir, "data", "nodwyn"))
	os.MkdirAll(paths.ConfigDir, 0o700)
	os.WriteFile(paths.ConfigFile, []byte("[mcp]\nruntime = \"podman\"\n"), 0o600)
	image := "ghcr.io/example/mcp-notes@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, s := range []config.MCPServer{{Name: "notes", Transport: config.TransportContainer, Image: image}, {Name: "chat", URL: "https://mcp.example.com/mcp"}} {
		if err := config.SaveMCPServer(config.MCPFile(paths), s); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLIHelper$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "NODWYN_TEST_CLI=1", quickExit, "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"),
			"PATH="+filepath.Dir(fake.Path)+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("mcp")
	if err != nil || !strings.Contains(out, "image not pulled: nodwyn mcp pull notes") || !strings.Contains(out, "remote") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if out, err := run("mcp", "pull", "notes"); err != nil || !strings.Contains(out, "notes is on this computer") || !fake.HasImage(image) {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if out, _ := run("mcp", "list"); !strings.Contains(out, "image present") {
		t.Fatalf("list after pull:\n%s", out)
	}
	if out, err := run("mcp", "pull", "chat"); err == nil || !strings.Contains(out, "remote server") {
		t.Fatalf("pulled a remote server: %v\n%s", err, out)
	}
}
