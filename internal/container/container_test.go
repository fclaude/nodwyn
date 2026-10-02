package container

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/egress"
	"github.com/fclaude/nodwyn/internal/mcp"
	fakecontainer "github.com/fclaude/nodwyn/test/fakes/container"
	fakemcp "github.com/fclaude/nodwyn/test/fakes/mcp"
)

func TestMain(m *testing.M) {
	fakecontainer.Main(fakemcp.Tool{Name: "echo", Reply: func(args map[string]any) (string, bool) { return "echo", false }})
	os.Exit(m.Run())
}

const image = "ghcr.io/example/mcp-notes@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func spec(network ...string) Spec {
	return Spec{
		Name: "nodwyn-notes-s1", Image: image, Command: "/usr/bin/notes-mcp", Args: []string{"--stdio", "--root", "/workspace/docs"},
		Mounts:  []Mount{{Source: "/home/me/work/docs", Target: "/workspace/docs"}, {Source: "/home/me/work/out", Target: "/workspace/out", Write: true}},
		Network: network, Labels: map[string]string{"nodwyn.session": "s1", "nodwyn.mcp": "notes"}, SecretEnv: "NOTES_TOKEN",
	}
}

// The run command is the isolation policy, flag for flag: nothing of the
// host's environment, no network or only the internal one, the mounts
// asked for and no other, never a pull.
func TestServerArgs(t *testing.T) {
	podman := Runtime{Kind: "podman", Path: "podman"}
	got := podman.serverArgs(spec(), "")
	want := []string{"run", "-i", "--name=nodwyn-notes-s1", "--label=nodwyn.mcp=notes", "--label=nodwyn.session=s1",
		"--rm", "--pull=never", "--log-driver=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=1g", "--pids-limit=256", "--http-proxy=false",
		"--tmpfs=/tmp:rw,nosuid,nodev,size=64m", "--network=none", "--env=NOTES_TOKEN",
		"--volume=/home/me/work/docs:/workspace/docs:ro,z", "--volume=/home/me/work/out:/workspace/out:rw,z",
		"--entrypoint=/usr/bin/notes-mcp", image, "--stdio", "--root", "/workspace/docs"}
	if !slices.Equal(got, want) {
		t.Fatalf("podman, no network:\n got %q\nwant %q", got, want)
	}
	docker := Runtime{Kind: "docker", Path: "docker"}
	got = docker.serverArgs(spec("api.example.com"), "http://10.89.0.2:3128")
	want = []string{"run", "-i", "--name=nodwyn-notes-s1", "--label=nodwyn.mcp=notes", "--label=nodwyn.session=s1",
		"--rm", "--pull=never", "--log-driver=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=1g", "--pids-limit=256",
		"--tmpfs=/tmp:rw,nosuid,nodev,size=64m", "--network=nodwyn-notes-s1-in",
		"--env=HTTP_PROXY=http://10.89.0.2:3128", "--env=HTTPS_PROXY=http://10.89.0.2:3128", "--env=http_proxy=http://10.89.0.2:3128", "--env=https_proxy=http://10.89.0.2:3128",
		"--env=NO_PROXY=localhost,127.0.0.1,::1", "--env=no_proxy=localhost,127.0.0.1,::1", "--env=NODE_USE_ENV_PROXY=1", "--env=NOTES_TOKEN",
		"--volume=/home/me/work/docs:/workspace/docs:ro,z", "--volume=/home/me/work/out:/workspace/out:rw,z",
		"--entrypoint=/usr/bin/notes-mcp", image, "--stdio", "--root", "/workspace/docs"}
	if !slices.Equal(got, want) {
		t.Fatalf("docker, networked:\n got %q\nwant %q", got, want)
	}
	bare := Spec{Name: "nodwyn-x-s1", Image: image}
	got = podman.serverArgs(bare, "")
	if got[len(got)-1] != image || slices.ContainsFunc(got, func(a string) bool {
		return strings.HasPrefix(a, "--entrypoint") || strings.HasPrefix(a, "--env") || strings.HasPrefix(a, "--volume")
	}) {
		t.Fatalf("bare: %q", got)
	}
}

func TestSidecarAndNetworkArgs(t *testing.T) {
	podman := Runtime{Kind: "podman", Path: "podman"}
	s := spec("api.example.com", "uploads.example.com:8443")
	got := podman.sidecarArgs(s, "/data/egress/nodwyn-egress-abc")
	want := []string{"run", "-i", "--name=nodwyn-notes-s1-egress", "--label=nodwyn.mcp=notes", "--label=nodwyn.session=s1",
		"--rm", "--pull=never", "--log-driver=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=64m", "--pids-limit=64", "--http-proxy=false",
		"--network=nodwyn-notes-s1-in", "--network=nodwyn-notes-s1-out", "--user=65534:65534",
		"--volume=/data/egress/nodwyn-egress-abc:/nodwyn-egress:ro,z", "--entrypoint=/nodwyn-egress", image,
		"-listen=:3128", "-lifeline", "-allow=api.example.com", "-allow=uploads.example.com:8443"}
	if !slices.Equal(got, want) {
		t.Fatalf("sidecar:\n got %q\nwant %q", got, want)
	}
	in, out := podman.networkArgs(s)
	if !slices.Equal(in, []string{"network", "create", "--internal", "--disable-dns", "--label=nodwyn.mcp=notes", "--label=nodwyn.session=s1", "nodwyn-notes-s1-in"}) ||
		!slices.Equal(out, []string{"network", "create", "--label=nodwyn.mcp=notes", "--label=nodwyn.session=s1", "nodwyn-notes-s1-out"}) {
		t.Fatalf("podman networks: %q %q", in, out)
	}
	in, _ = Runtime{Kind: "docker"}.networkArgs(s)
	if slices.Contains(in, "--disable-dns") || !slices.Contains(in, "--internal") {
		t.Fatalf("docker internal network: %q", in)
	}
	if got := podman.rmArgs("a", "b"); !slices.Equal(got, []string{"rm", "--force", "--ignore", "--time=0", "a", "b"}) {
		t.Fatalf("podman rm: %q", got)
	}
	if got := (Runtime{Kind: "docker"}).rmArgs("a"); !slices.Equal(got, []string{"rm", "--force", "a"}) {
		t.Fatalf("docker rm: %q", got)
	}
}

// Start and Close against the fake runtime: the image is checked first,
// the proxy runs before the server and reports through Report, the
// server's runtime gets a short environment with the secret and nothing
// else of the host's, and Close removes the containers and the networks.
func TestStartAndClose(t *testing.T) {
	fake := fakecontainer.New(t, "podman")
	rt := Runtime{Kind: "podman", Path: fake.Path}
	t.Setenv("NODWYN_HOST_CANARY", "do-not-pass")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := rt.Start(ctx, Options{Spec: Spec{Name: "nodwyn-notes-s1", Image: image}}); err != ErrImageMissing {
		t.Fatalf("missing image: %v", err)
	}
	if len(fake.Find("run")) != 0 {
		t.Fatal("ran without the image")
	}
	fake.AddImage(image)
	fake.SetReports(`{"method":"CONNECT","host":"api.example.com","port":443,"allowed":true}`, `{"method":"CONNECT","host":"evil.example.net","port":443,"allowed":false,"reason":"not on the server's network list"}`)
	fake.SetStderr(strings.Repeat("noise ", 4000) + "the end\n")
	egressBin := filepath.Join(t.TempDir(), "nodwyn-egress")
	os.WriteFile(egressBin, []byte("#!/bin/false\n"), 0o755)
	egressDir := filepath.Join(t.TempDir(), "egress")
	reports := make(chan egress.Report, 4)
	s := Spec{Name: "nodwyn-notes-s1", Image: image, Network: []string{"api.example.com"}, SecretEnv: "NOTES_TOKEN"}
	server, err := rt.Start(ctx, Options{Spec: s, Egress: egressBin, EgressDir: egressDir,
		Inject: func(cmd *exec.Cmd) error { cmd.Env = append(cmd.Env, "NOTES_TOKEN=sk-notes-0123456789"); return nil },
		Report: func(r egress.Report) { reports <- r }})
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewStdio(server.Stdin(), server.Stdout(), server.Ended)
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if tools, err := client.Tools(ctx); err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools: %+v %v", tools, err)
	}
	var order []string
	for _, inv := range fake.Calls() {
		order = append(order, strings.Join(inv.Args[:2], " "))
		for _, kv := range inv.Env {
			if strings.HasPrefix(kv, "NODWYN_HOST_CANARY=") {
				t.Fatalf("the host environment reached %q", inv.Args)
			}
		}
	}
	want := []string{"image inspect", "image inspect", "network create", "network create", "run -i", "container inspect", "run -i"}
	if !slices.Equal(order, want) {
		t.Fatalf("order:\n got %q\nwant %q", order, want)
	}
	runs := fake.Find("run", "-i", "--name=nodwyn-notes-s1")
	if !slices.Contains(runs[0].Env, "NOTES_TOKEN=sk-notes-0123456789") || !slices.Contains(runs[0].Args, "--env=NOTES_TOKEN") || !slices.Contains(runs[0].Args, "--env=HTTPS_PROXY=http://10.89.0.2:3128") {
		t.Fatalf("server run: %q env %q", runs[0].Args, runs[0].Env)
	}
	for _, inv := range fake.Calls() {
		if strings.Contains(strings.Join(inv.Args, " "), "sk-notes") {
			t.Fatalf("the secret is in an argument list: %q", inv.Args)
		}
		if !slices.Contains(inv.Args, "--name=nodwyn-notes-s1") {
			if slices.ContainsFunc(inv.Env, func(kv string) bool { return strings.HasPrefix(kv, "NOTES_TOKEN=") }) {
				t.Fatalf("the secret reached %q", inv.Args)
			}
		}
	}
	sidecar := fake.Find("run", "-i", "--name=nodwyn-notes-s1-egress")[0].Args
	mounted := ""
	for _, a := range sidecar {
		if v, ok := strings.CutPrefix(a, "--volume="); ok {
			mounted = strings.Split(v, ":")[0]
		}
	}
	if filepath.Dir(mounted) != egressDir || mounted == egressBin {
		t.Fatalf("the proxy mounts %s, not a private copy", mounted)
	}
	for range 2 {
		select {
		case r := <-reports:
			if r.Host == "" || r.Listening != "" {
				t.Fatalf("report %+v", r)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no proxy report")
		}
	}
	server.Close()
	if running := fake.Running(); len(running) != 0 {
		t.Fatalf("still running: %v", running)
	}
	if len(fake.Find("rm", "--force", "--ignore", "--time=0", "nodwyn-notes-s1", "nodwyn-notes-s1-egress")) != 1 || len(fake.Find("network", "rm", "nodwyn-notes-s1-in", "nodwyn-notes-s1-out")) != 1 {
		t.Fatalf("not removed: %q", fake.Calls())
	}
	if text := server.Stderr(); len(text) > stderrKept+4 || !strings.HasPrefix(text, "…") || !strings.HasSuffix(text, "the end\n") {
		t.Fatalf("stderr kept %d bytes: %.40q", len(text), text)
	}
	if _, err := client.Tools(ctx); err == nil {
		t.Fatal("a call after Close succeeded")
	}
}

func TestTailKeepsTheEnd(t *testing.T) {
	tail := NewTail(8)
	tail.Write([]byte("0123"))
	if tail.String() != "0123" {
		t.Fatal(tail.String())
	}
	tail.Write([]byte("456789abc"))
	if tail.String() != "…56789abc" {
		t.Fatalf("%q", tail.String())
	}
}

func TestPrivateCopyIsByContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "nodwyn-egress")
	os.WriteFile(src, []byte("one"), 0o755)
	a, err := privateCopy(src, filepath.Join(dir, "copies"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := privateCopy(src, filepath.Join(dir, "copies"))
	os.WriteFile(src, []byte("two"), 0o755)
	c, _ := privateCopy(src, filepath.Join(dir, "copies"))
	if a != b || a == c {
		t.Fatalf("%s %s %s", a, b, c)
	}
	// A copy someone changed is replaced, not mounted.
	os.WriteFile(c, []byte("tampered"), 0o755)
	if d, _ := privateCopy(src, filepath.Join(dir, "copies")); d != c {
		t.Fatal(d)
	}
	if got, _ := os.ReadFile(c); string(got) != "two" {
		t.Fatalf("copy holds %q", got)
	}
	if info, _ := os.Stat(c); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", info.Mode())
	}
}

func TestFind(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", dir)
	rt, err := Find("")
	if err != nil || rt.Kind != "docker" {
		t.Fatalf("%+v %v", rt, err)
	}
	if _, err := Find("podman"); err == nil || !strings.Contains(err.Error(), "podman is not on PATH") {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := Find(""); err == nil || !strings.Contains(err.Error(), "neither podman nor docker") {
		t.Fatal(err)
	}
}
