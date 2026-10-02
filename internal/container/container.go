// Package container runs a contained MCP server (ADR 23 W3b) with podman
// or docker, and nothing else. The server runs from an image pinned by
// digest that is already on the computer (a session never pulls), with no
// capability, a read-only root, no host environment, and either no network
// at all or an internal network whose only way out is nodwyn's egress
// proxy, in a sidecar container, which lets through the hosts the person
// listed and reports every attempt. nodwyn talks to the server over its
// standard input and output (MCP's stdio transport), so no port is
// published.
//
// Every runtime command gets a fixed, short environment the runtime needs
// to find its socket and storage; a container gets only the variables named
// in its arguments.
package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

// Runtime is the container runtime nodwyn drives.
type Runtime struct {
	Kind string // "podman" or "docker"
	Path string // its executable
}

// Find returns the runtime kind names ("podman" or "docker"), or, for "",
// podman if it is on PATH, else docker.
func Find(kind string) (Runtime, error) {
	kinds := []string{"podman", "docker"}
	if kind != "" {
		kinds = []string{kind}
	}
	for _, k := range kinds {
		if path, err := exec.LookPath(k); err == nil {
			return Runtime{Kind: k, Path: path}, nil
		}
	}
	if kind != "" {
		return Runtime{}, fmt.Errorf("%s is not on PATH; contained MCP servers need it ([mcp] runtime in config.toml)", kind)
	}
	return Runtime{}, errors.New("neither podman nor docker is on PATH; contained MCP servers need one")
}

// runtimeEnv are the variables a runtime needs to find its socket, its
// storage and its configuration. Nothing else of nodwyn's environment
// reaches it, and none of these reaches a container.
var runtimeEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "DBUS_SESSION_BUS_ADDRESS",
	"CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINERS_CONF", "CONTAINERS_STORAGE_CONF", "CONTAINERS_REGISTRIES_CONF", "REGISTRY_AUTH_FILE",
	"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY",
}

// pullEnv are added for a pull, which reaches a registry the way the
// person's other tools do.
var pullEnv = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"}

func environ(names []string) []string {
	var env []string
	for _, name := range names {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// command prepares a runtime command. It runs in its own process group, so
// a terminal's Ctrl-C reaches nodwyn, which stops its containers itself.
func (r Runtime) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.Path, args...)
	cmd.Env = environ(runtimeEnv)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// output runs a runtime command and returns its standard output; an error
// carries the end of its standard error.
func (r Runtime) output(ctx context.Context, args ...string) (string, error) {
	cmd := r.command(ctx, args...)
	var stdout bytes.Buffer
	stderr := NewTail(2 << 10)
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return "", fmt.Errorf("%s %s: %s", r.Kind, args[0], detail)
		}
		return "", fmt.Errorf("%s %s: %w", r.Kind, args[0], err)
	}
	return stdout.String(), nil
}

// ImagePresent reports whether image (name@sha256:…) is on this computer.
// A runtime that cannot answer is an error, not an absent image.
func (r Runtime) ImagePresent(ctx context.Context, image string) (bool, error) {
	out, err := r.output(ctx, "image", "inspect", "--format={{json .RepoDigests}}", image)
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "image not known") || strings.Contains(msg, "no such image") || strings.Contains(msg, "no such object") {
			return false, nil
		}
		return false, err
	}
	// Looked up by digest, the image is that content; check the runtime
	// agrees rather than trust the lookup alone.
	_, digest, _ := strings.Cut(image, "@")
	var digests []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &digests); err != nil {
		return false, fmt.Errorf("%s image inspect: %w", r.Kind, err)
	}
	for _, d := range digests {
		if strings.HasSuffix(d, "@"+digest) {
			return true, nil
		}
	}
	return false, nil
}

// Pull fetches image by its digest, writing the runtime's progress to out.
// It is the only command that downloads anything, and it runs only when
// the person asks (nodwyn mcp pull, the console's Pull).
func (r Runtime) Pull(ctx context.Context, image string, out io.Writer) error {
	cmd := r.command(ctx, "pull", image)
	cmd.Env = append(cmd.Env, environ(pullEnv)...)
	stderr := NewTail(4 << 10)
	cmd.Stdout = out
	cmd.Stderr = io.MultiWriter(out, stderr)
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return fmt.Errorf("%s pull: %s", r.Kind, detail)
		}
		return fmt.Errorf("%s pull: %w", r.Kind, err)
	}
	return nil
}

// Tail keeps the last bytes written to it, for diagnostics that must not
// grow without bound (a server's standard error).
type Tail struct {
	max   int
	buf   []byte
	total int64
	mu    sync.Mutex
}

// NewTail keeps the last max bytes.
func NewTail(max int) *Tail { return &Tail{max: max} }

// Write keeps the end of p; it never fails.
func (t *Tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.total += int64(len(p))
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

// String is what was kept, marked when the beginning was dropped.
func (t *Tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.total > int64(len(t.buf)) {
		return "…" + string(t.buf)
	}
	return string(t.buf)
}
