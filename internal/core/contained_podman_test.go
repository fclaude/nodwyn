package core

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/container"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// TestContainedWithPodman runs contained servers with the real podman (or
// NODWYN_CONTAINER_TEST_RUNTIME=docker), opt-in: NODWYN_CONTAINER_TESTS=1.
// It pulls nothing: the image is
// NODWYN_CONTAINER_TEST_IMAGE (name@sha256:…) or the first local image
// with a digest, used only as a root file system; the server is
// test/fakes/mcp/stdio, built static and mounted from the workspace, and
// its tools probe the isolation from inside. With NODWYN_CONTAINER_TESTS_NET=1
// it also reaches example.com, a listed host, through the proxy.
func TestContainedWithPodman(t *testing.T) {
	t.Parallel()
	if os.Getenv("NODWYN_CONTAINER_TESTS") != "1" {
		t.Skip("set NODWYN_CONTAINER_TESTS=1 to run contained servers with podman")
	}
	kind := os.Getenv("NODWYN_CONTAINER_TEST_RUNTIME")
	if kind == "" {
		kind = "podman"
	}
	rt, err := container.Find(kind)
	if err != nil {
		t.Skip(err)
	}
	image := os.Getenv("NODWYN_CONTAINER_TEST_IMAGE")
	if image == "" {
		image = localImage(t, rt)
	}
	root := t.TempDir()
	repo := filepath.Join(root, "work")
	for _, dir := range []string{"bin", "docs"} {
		os.MkdirAll(filepath.Join(repo, dir), 0o755)
	}
	buildStatic(t, "./test/fakes/mcp/stdio", filepath.Join(repo, "bin", "fakemcp"))
	egressBin := filepath.Join(root, "nodwyn-egress")
	buildStatic(t, "./cmd/nodwyn-egress", egressBin)
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	os.MkdirAll(paths.ConfigDir, 0o700)
	store, err := keychain.Create(filepath.Join(root, "keychain"), keychain.FastKDF, []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	store.Add("probe-key", []byte("sk-probe-canary-0123456789abcdef"))
	row, _ := binding.Exact("probe-key", "example.com", "*", "env:PROBE_TOKEN")
	if err := binding.AppendExact(context.Background(), paths.Bindings, row); err != nil {
		t.Fatal(err)
	}
	options := func() Options {
		cfg := config.Default()
		cfg.MCP.Runtime, cfg.MCP.Egress = kind, egressBin
		return Options{Config: cfg, Repo: repo, Paths: paths, Keychain: store, Version: "test"}
	}
	tools := map[string]config.MCPTool{}
	for _, name := range []string{"fetch", "dial", "resolve", "env", "write"} {
		tools[name] = config.MCPTool{Effect: config.EffectRead, On: true}
	}
	open := func(server config.MCPServer) (*Session, *bytes.Buffer) {
		t.Helper()
		_, manifest, err := InspectMCP(options(), server)
		if err != nil {
			t.Fatal(err)
		}
		server.Manifest = manifest
		if err := config.SaveMCPServer(config.MCPFile(paths), server); err != nil {
			t.Fatal(err)
		}
		srv := fake.NewT(t)
		t.Cleanup(srv.Close)
		o := options()
		o.Config.Control.URL, o.Config.Control.Model, o.Provider = srv.URL, "test-model", llm.NewOllama(srv.URL)
		logb := &bytes.Buffer{}
		o.LogWriter = logb
		s, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		if st := s.MCP(); len(st) != 1 || st[0].State != "ready" {
			t.Fatalf("status: %+v", st)
		}
		return s, logb
	}
	call := func(s *Session, src string) string {
		t.Helper()
		v, err := s.Eval(context.Background(), src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		return v.S
	}
	gone := func(s *Session) {
		t.Helper()
		for _, args := range [][]string{{"ps", "-a", "--format={{.Names}}", "--filter=label=nodwyn.session=" + s.ID()}, {"network", "ls", "--format={{.Name}}", "--filter=label=nodwyn.session=" + s.ID()}} {
			out, err := exec.Command(rt.Path, args...).Output()
			if err != nil || strings.TrimSpace(string(out)) != "" {
				t.Fatalf("left behind (%s): %q %v", args[0], out, err)
			}
		}
	}

	// No network at all: nothing resolves, nothing connects, the image's
	// files and a read-only mount cannot be written, no variable leaks in.
	box := config.MCPServer{Name: "box", Transport: config.TransportContainer, Image: image, Command: "/workspace/bin/fakemcp", Mounts: []string{"bin", "docs"},
		Workspaces: []string{repo}, Tools: tools}
	s, _ := open(box)
	for src, want := range map[string]string{
		`(box__fetch "https://example.com/")`:       "error:",
		`(box__dial "1.1.1.1:443")`:                 "error:",
		`(box__resolve "example.com")`:              "error:",
		`(box__env "HOME_CANARY")`:                  "unset",
		`(box__write "/workspace/docs/x" "y")`:      "read-only file system",
		`(box__write "/etc/nodwyn-probe" "y")`:      "read-only file system",
		`(box__write "/tmp/nodwyn-probe" "y")`:      "written",
		`(box__fetch "http://169.254.169.254/")`:    "error:",
		`(box__dial "host.containers.internal:22")`: "error:",
	} {
		if got := call(s, src); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want %q", src, got, want)
		}
	}
	s.Close()
	gone(s)

	// A network list: the server reaches only the proxy, which lets
	// through example.com and nothing else; its key is in its environment.
	box.Network = []string{"example.com"}
	box.Credential, box.Position = "probe-key", "env:PROBE_TOKEN"
	s, logb := open(box)
	for src, want := range map[string]string{
		`(box__env "PROBE_TOKEN")`:                 "set",
		`(box__fetch "https://www.google.com/")`:   "Forbidden",
		`(box__fetch "http://www.google.com/")`:    "status 403",
		`(box__fetch "https://example.com:8443/")`: "Forbidden",
		`(box__dial "1.1.1.1:443")`:                "error:",
		`(box__dial "example.com:443")`:            "error:",
		`(box__resolve "example.com")`:             "error:",
	} {
		if got := call(s, src); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want %q", src, got, want)
		}
	}
	if os.Getenv("NODWYN_CONTAINER_TESTS_NET") == "1" {
		if got := call(s, `(box__fetch "https://example.com/")`); got != "status 200" {
			t.Errorf("the listed host: %q", got)
		}
	}
	s.Close()
	gone(s)
	if strings.Contains(logb.String(), "canary") {
		t.Fatal("the key reached the log")
	}
	evs, err := log.Read(bytes.NewReader(logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	refused := map[string]bool{}
	for _, ev := range evs {
		var e log.MCPEgress
		if ev.Kind == log.KindMCPEgress && ev.Decode(&e) == nil && !e.Allowed {
			refused[e.Host] = true
		}
	}
	if !refused["www.google.com"] || !refused["example.com"] { // example.com:8443
		t.Fatalf("refusals recorded: %v", refused)
	}
}

// localImage is the first image on this computer with a digest.
func localImage(t *testing.T, rt container.Runtime) string {
	t.Helper()
	out, err := exec.Command(rt.Path, "images", "--format={{.Repository}}@{{.Digest}}").Output()
	if err != nil {
		t.Skip(err)
	}
	for _, line := range strings.Fields(string(out)) {
		if strings.HasPrefix(line, "<none>") || !strings.Contains(line, "@sha256:") {
			continue
		}
		if ok, err := rt.ImagePresent(context.Background(), line); err == nil && ok {
			return line
		}
	}
	t.Skip("no local image with a digest: pull one, or set NODWYN_CONTAINER_TEST_IMAGE")
	return ""
}

// buildStatic builds a package of this module for Linux, with no cgo.
func buildStatic(t *testing.T, pkg, out string) {
	t.Helper()
	gocmd, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command to build ", pkg, " with")
	}
	_, file, _, _ := runtime.Caller(0)
	cmd := exec.Command(gocmd, "build", "-o", out, pkg)
	cmd.Dir = filepath.Join(filepath.Dir(file), "..", "..")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, b)
	}
}
