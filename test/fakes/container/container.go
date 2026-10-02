// Package container is a fake container runtime for tests (ADR 23 W3b):
// an executable script that runs the test binary again, which then acts as
// podman or docker would for the commands nodwyn gives: image inspect,
// pull, network create and rm, container inspect, run and rm. Running a
// server speaks MCP over stdio with the tools the test binary passed to
// Main; running the egress proxy prints a listening line and the reports
// the test scripted, then waits to be removed. Every invocation's
// arguments and environment are recorded. Nothing is pulled, nothing is
// contained and no network is touched.
//
// A test package that uses it calls Main first from its TestMain:
//
//	func TestMain(m *testing.M) {
//		fakecontainer.Main(tools...)
//		os.Exit(m.Run())
//	}
package container

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	fakemcp "github.com/fclaude/nodwyn/test/fakes/mcp"
)

// stateVar names the fake's state folder in the script it writes; the test
// binary is the fake when it is set.
const stateVar = "NODWYN_FAKE_RUNTIME"

// Runtime is one fake runtime and its state.
type Runtime struct {
	Dir  string // state: images, recorded calls, running containers
	Path string // the executable nodwyn runs
	Kind string // "podman" or "docker"
}

// Invocation is one run of the runtime.
type Invocation struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
}

// New writes a fake runtime of kind into a temporary folder.
func New(t testing.TB, kind string) *Runtime {
	t.Helper()
	r, err := Install(t.TempDir(), kind)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.StopAll)
	return r
}

// Install writes a fake runtime of kind into dir, for a program that is not
// a test (the browser demo); it calls StopAll when it is done.
func Install(dir, kind string) (*Runtime, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"images", "running", "containers"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	path := filepath.Join(dir, kind)
	// A race-enabled binary otherwise sleeps a second as it exits.
	script := fmt.Sprintf("#!/bin/sh\nGORACE=atexit_sleep_ms=0 %s=%s exec %s \"$@\"\n", stateVar, shellQuote(dir), shellQuote(exe))
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return nil, err
	}
	return &Runtime{Dir: dir, Path: path, Kind: kind}, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// AddImage makes ref (name@sha256:…) present, as a pull would.
func (r *Runtime) AddImage(ref string) {
	_ = os.WriteFile(filepath.Join(r.Dir, "images", imageKey(ref)), []byte(ref), 0o600)
}

// HasImage reports whether ref is present.
func (r *Runtime) HasImage(ref string) bool {
	_, err := os.Stat(filepath.Join(r.Dir, "images", imageKey(ref)))
	return err == nil
}

func imageKey(ref string) string {
	_, digest, _ := strings.Cut(ref, "@")
	return strings.ReplaceAll(digest, ":", "-")
}

// SetReports scripts what the egress proxy reports after it listens, one
// JSON line each.
func (r *Runtime) SetReports(lines ...string) {
	_ = os.WriteFile(filepath.Join(r.Dir, "reports"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// SetStderr scripts what a server writes to its standard error when it
// starts.
func (r *Runtime) SetStderr(text string) {
	_ = os.WriteFile(filepath.Join(r.Dir, "stderr"), []byte(text), 0o600)
}

// Calls are the invocations so far, in order.
func (r *Runtime) Calls() []Invocation {
	raw, _ := os.ReadFile(filepath.Join(r.Dir, "calls.jsonl"))
	var out []Invocation
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var inv Invocation
		if json.Unmarshal([]byte(line), &inv) == nil {
			out = append(out, inv)
		}
	}
	return out
}

// Find returns the invocations whose arguments start with prefix.
func (r *Runtime) Find(prefix ...string) []Invocation {
	var out []Invocation
	for _, inv := range r.Calls() {
		if len(inv.Args) >= len(prefix) && slices.Equal(inv.Args[:len(prefix)], prefix) {
			out = append(out, inv)
		}
	}
	return out
}

// Running are the names of containers still running.
func (r *Runtime) Running() []string {
	entries, _ := os.ReadDir(filepath.Join(r.Dir, "running"))
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// ToolCalls are the tools/call requests the fake servers received.
func (r *Runtime) ToolCalls() []fakemcp.Call {
	raw, _ := os.ReadFile(filepath.Join(r.Dir, "toolcalls.jsonl"))
	var out []fakemcp.Call
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var c fakemcp.Call
		if json.Unmarshal([]byte(line), &c) == nil {
			out = append(out, c)
		}
	}
	return out
}

// StopAll ends every container still running.
func (r *Runtime) StopAll() {
	for _, name := range r.Running() {
		stop(r.Dir, name)
	}
}

// Main runs the test binary as the fake runtime when the script started it,
// and exits; otherwise it returns at once. tools are what a server offers.
func Main(tools ...fakemcp.Tool) {
	dir := os.Getenv(stateVar)
	if dir == "" {
		return
	}
	os.Exit(run(dir, os.Args[1:], tools))
}

func run(dir string, args []string, tools []fakemcp.Tool) int {
	record(dir, "calls.jsonl", Invocation{Args: args, Env: environ()})
	if len(args) == 0 {
		return 125
	}
	switch args[0] {
	case "image":
		ref := args[len(args)-1]
		if _, err := os.Stat(filepath.Join(dir, "images", imageKey(ref))); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s: image not known\n", ref)
			return 125
		}
		name, digest, _ := strings.Cut(ref, "@")
		fmt.Printf("[%q]\n", "docker.io/"+name+"@"+digest)
		return 0
	case "pull":
		ref := args[len(args)-1]
		fmt.Println("Trying to pull " + ref + "...")
		_ = os.WriteFile(filepath.Join(dir, "images", imageKey(ref)), []byte(ref), 0o600)
		return 0
	case "network":
		if len(args) > 1 && args[1] == "create" {
			fmt.Println(args[len(args)-1])
		}
		return 0
	case "container":
		// container inspect: the proxy's address on each of its networks.
		raw, err := os.ReadFile(filepath.Join(dir, "containers", args[len(args)-1]))
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error: no such container")
			return 125
		}
		nets := map[string]map[string]string{}
		for i, n := range strings.Fields(string(raw)) {
			nets[n] = map[string]string{"IPAddress": fmt.Sprintf("10.89.%d.2", i)}
		}
		out, _ := json.Marshal(nets)
		fmt.Println(string(out))
		return 0
	case "rm":
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") {
				stop(dir, a)
			}
		}
		return 0
	case "run":
		return runContainer(dir, args[1:], tools)
	}
	fmt.Fprintf(os.Stderr, "fake runtime: %s is not faked\n", args[0])
	return 125
}

// runContainer acts out a run: flags are --flag=value or bare, the first
// other word is the image, the rest its arguments.
func runContainer(dir string, args []string, tools []fakemcp.Tool) int {
	var name, entrypoint string
	var networks []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			break
		}
		key, value, _ := strings.Cut(a, "=")
		switch key {
		case "--name":
			name = value
		case "--network":
			networks = append(networks, value)
		case "--entrypoint":
			entrypoint = value
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "containers", name), []byte(strings.Join(networks, " ")), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "running", name), []byte(strconv.Itoa(os.Getpid())), 0o600)
	defer os.Remove(filepath.Join(dir, "running", name))
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	if entrypoint == "/nodwyn-egress" {
		fmt.Println(`{"listening":"[::]:3128"}`)
		if raw, err := os.ReadFile(filepath.Join(dir, "reports")); err == nil {
			os.Stdout.Write(raw)
		}
		<-signals
		return 143
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "stderr")); err == nil {
		os.Stderr.Write(raw)
	}
	server := fakemcp.NewStdio(tools...)
	done := make(chan error, 1)
	go func() { done <- server.Serve(os.Stdin, os.Stdout) }()
	var code int
	select {
	case <-done:
	case <-signals:
		code = 143
	}
	for _, c := range server.Recorded() {
		record(dir, "toolcalls.jsonl", c)
	}
	return code
}

// stop ends a running container, as rm --force does.
func stop(dir, name string) {
	raw, err := os.ReadFile(filepath.Join(dir, "running", name))
	if err != nil {
		return
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	for range 100 {
		if _, err := os.Stat(filepath.Join(dir, "running", name)); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func environ() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, stateVar+"=") && !strings.HasPrefix(kv, "GORACE=") {
			out = append(out, kv)
		}
	}
	return out
}

// record appends one JSON line; a line this short is written whole.
func record(dir, file string, v any) {
	line, _ := json.Marshal(v)
	f, err := os.OpenFile(filepath.Join(dir, file), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	w.Write(append(line, '\n'))
	w.Flush()
}
