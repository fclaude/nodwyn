package container

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/nodwyn/internal/egress"
)

// Limits a contained server and its proxy run with.
const (
	serverMemory  = "1g"
	serverPids    = "256"
	sidecarMemory = "64m"
	sidecarPids   = "64"
	proxyPort     = "3128"
	// stderrKept is how much of a server's standard error is kept for
	// diagnostics.
	stderrKept = 8 << 10
)

// Mount is a host folder a server sees.
type Mount struct {
	Source string // absolute, on the host
	Target string // in the container
	Write  bool
}

// Spec is one contained server as nodwyn runs it.
type Spec struct {
	// Name is the server container's name. Its proxy, when it has one, is
	// Name-egress, on the networks Name-in (internal) and Name-out.
	Name    string
	Image   string // name@sha256:…, already on the computer
	Command string // replaces the image's entrypoint; "" keeps it
	Args    []string
	Mounts  []Mount
	// Network is the allowlist (host, or host:port); empty: no network.
	Network []string
	Labels  map[string]string
	// SecretEnv is a variable passed by name only: its value is put in the
	// runtime's own environment by Options.Inject, so it is never in an
	// argument list another process could read.
	SecretEnv string
}

func (s Spec) sidecar() string  { return s.Name + "-egress" }
func (s Spec) internal() string { return s.Name + "-in" }
func (s Spec) outside() string  { return s.Name + "-out" }
func (s Spec) networked() bool  { return len(s.Network) > 0 }
func (s Spec) proxyURL(ip string) string {
	return "http://" + net.JoinHostPort(ip, proxyPort)
}

// labels are the --label arguments, sorted.
func (s Spec) labels() []string {
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, "--label="+k+"="+s.Labels[k])
	}
	return out
}

// isolation are the arguments every container nodwyn runs gets: removed
// when it stops, never pulled, nothing logged by the runtime (the MCP
// conversation is the session's to record), a read-only root, no
// capability, no privilege gained through setuid, bounded memory and
// processes, and for podman no copy of the host's proxy variables.
func (r Runtime) isolation(memory, pids string) []string {
	args := []string{"--rm", "--pull=never", "--log-driver=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=" + memory, "--pids-limit=" + pids}
	if r.Kind == "podman" {
		args = append(args, "--http-proxy=false")
	}
	return args
}

// serverArgs is the run command of the server. network is "none" or the
// internal network; proxy is the egress proxy's URL on it.
func (r Runtime) serverArgs(s Spec, proxy string) []string {
	args := []string{"run", "-i", "--name=" + s.Name}
	args = append(args, s.labels()...)
	args = append(args, r.isolation(serverMemory, serverPids)...)
	args = append(args, "--tmpfs=/tmp:rw,nosuid,nodev,size=64m")
	if s.networked() {
		args = append(args, "--network="+s.internal())
		for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			args = append(args, "--env="+name+"="+proxy)
		}
		args = append(args, "--env=NO_PROXY=localhost,127.0.0.1,::1", "--env=no_proxy=localhost,127.0.0.1,::1", "--env=NODE_USE_ENV_PROXY=1")
	} else {
		args = append(args, "--network=none")
	}
	if s.SecretEnv != "" {
		args = append(args, "--env="+s.SecretEnv)
	}
	for _, m := range s.Mounts {
		mode := "ro"
		if m.Write {
			mode = "rw"
		}
		// z: on SELinux the folder is labelled so containers may use it;
		// elsewhere it changes nothing.
		args = append(args, "--volume="+m.Source+":"+m.Target+":"+mode+",z")
	}
	if s.Command != "" {
		args = append(args, "--entrypoint="+s.Command)
	}
	args = append(args, s.Image)
	return append(args, s.Args...)
}

// sidecarArgs is the run command of the egress proxy: the server's own
// image (already here, so nothing more is pulled) as a root file system,
// with the static nodwyn-egress mounted read-only as its entrypoint, run as
// nobody, on the internal network and the outside one. Its standard input
// is held open by nodwyn: it stops when that ends, so a proxy does not
// outlive a nodwyn that was killed.
func (r Runtime) sidecarArgs(s Spec, binary string) []string {
	args := []string{"run", "-i", "--name=" + s.sidecar()}
	args = append(args, s.labels()...)
	args = append(args, r.isolation(sidecarMemory, sidecarPids)...)
	args = append(args, "--network="+s.internal(), "--network="+s.outside(), "--user=65534:65534",
		"--volume="+binary+":/nodwyn-egress:ro,z", "--entrypoint=/nodwyn-egress", s.Image, "-listen=:"+proxyPort, "-lifeline")
	for _, entry := range s.Network {
		args = append(args, "-allow="+entry)
	}
	return args
}

// networkArgs creates the server's networks: one with no route out and,
// for podman, no DNS (docker answers no outside name on an internal
// network), and one the proxy reaches the world through.
func (r Runtime) networkArgs(s Spec) (internal, outside []string) {
	internal = []string{"network", "create", "--internal"}
	if r.Kind == "podman" {
		internal = append(internal, "--disable-dns")
	}
	internal = append(append(internal, s.labels()...), s.internal())
	outside = append(append([]string{"network", "create"}, s.labels()...), s.outside())
	return internal, outside
}

// rmArgs removes containers at once, running or not.
func (r Runtime) rmArgs(names ...string) []string {
	args := []string{"rm", "--force"}
	if r.Kind == "podman" {
		args = append(args, "--ignore", "--time=0")
	}
	return append(args, names...)
}

// Options are what Start needs besides the spec.
type Options struct {
	Spec
	// Egress is the nodwyn-egress binary; EgressDir is a private folder a
	// copy of it is kept in, which the proxy container mounts. Both are
	// needed only for a server with a network list.
	Egress, EgressDir string
	// Inject puts the value of Spec.SecretEnv in the run command's
	// environment (a keychain sink); nil when there is none.
	Inject func(*exec.Cmd) error
	// Report receives every attempt the proxy reports.
	Report func(egress.Report)
}

// ErrImageMissing is returned by Start when the image is not on this
// computer: a session does not pull.
var ErrImageMissing = errors.New("its image is not on this computer")

// Server is a running contained server.
type Server struct {
	rt      Runtime
	spec    Spec
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     io.Reader
	stderr  *Tail
	exited  chan struct{}
	waitErr error

	sidecar     *exec.Cmd
	sidecarIn   io.WriteCloser // the proxy's lifeline
	sidecarDone chan struct{}
	networks    bool

	closeOnce sync.Once
}

// Start runs the server, and first its proxy when it has a network list.
// ctx bounds starting; the containers run until Close.
func (r Runtime) Start(ctx context.Context, o Options) (_ *Server, err error) {
	present, err := r.ImagePresent(ctx, o.Image)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, ErrImageMissing
	}
	s := &Server{rt: r, spec: o.Spec, stderr: NewTail(stderrKept), exited: make(chan struct{})}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	proxy := ""
	if o.networked() {
		if proxy, err = s.startProxy(ctx, o); err != nil {
			return nil, err
		}
	}
	cmd := r.command(context.Background(), r.serverArgs(o.Spec, proxy)...)
	if o.SecretEnv != "" {
		if o.Inject == nil {
			return nil, errors.New("container: a secret variable without a way to inject it")
		}
		if err := o.Inject(cmd); err != nil {
			return nil, err
		}
	}
	if s.in, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	cmd.Stderr = s.stderr
	out, err := startReading(cmd)
	if err != nil {
		return nil, fmt.Errorf("%s run: %w", r.Kind, err)
	}
	s.out, s.cmd = out, cmd
	go func() {
		s.waitErr = cmd.Wait()
		close(s.exited)
	}()
	return s, nil
}

// startReading starts cmd with a standard output the caller reads to its
// end. Unlike StdoutPipe, Wait does not close it, so nothing the process
// wrote before it exited is lost.
func startReading(cmd *exec.Cmd) (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = w
	err = cmd.Start()
	w.Close()
	if err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// startProxy creates the networks, starts the proxy, waits until it
// listens and returns its address on the internal network.
func (s *Server) startProxy(ctx context.Context, o Options) (string, error) {
	r := s.rt
	if o.Egress == "" {
		return "", errors.New("container: no nodwyn-egress to run the proxy with")
	}
	binary, err := privateCopy(o.Egress, o.EgressDir)
	if err != nil {
		return "", err
	}
	internal, outside := r.networkArgs(o.Spec)
	s.networks = true
	for _, args := range [][]string{internal, outside} {
		if _, err := r.output(ctx, args...); err != nil {
			return "", err
		}
	}
	cmd := r.command(context.Background(), r.sidecarArgs(o.Spec, binary)...)
	stderr := NewTail(2 << 10)
	cmd.Stderr = stderr
	if s.sidecarIn, err = cmd.StdinPipe(); err != nil {
		return "", err
	}
	stdout, err := startReading(cmd)
	if err != nil {
		return "", fmt.Errorf("%s run: %w", r.Kind, err)
	}
	s.sidecar, s.sidecarDone = cmd, make(chan struct{})
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 4<<10), 64<<10)
	listening := make(chan bool, 1)
	// The proxy's reports are read to their end, then the run command is
	// waited for.
	go func() {
		defer close(s.sidecarDone)
		defer func() { _ = cmd.Wait() }()
		defer stdout.Close()
		first := true
		for lines.Scan() {
			var rep egress.Report
			if json.Unmarshal(lines.Bytes(), &rep) != nil {
				continue
			}
			if first {
				first = false
				listening <- rep.Listening != ""
				continue
			}
			if rep.Listening == "" && o.Report != nil {
				o.Report(rep)
			}
		}
		_, _ = io.Copy(io.Discard, stdout) // a line too long ends the scan, not the reading
		if first {
			listening <- false
		}
	}()
	select {
	case ok := <-listening:
		if !ok {
			return "", fmt.Errorf("the egress proxy did not start: %s", strings.TrimSpace(stderr.String()))
		}
	case <-ctx.Done():
		return "", fmt.Errorf("the egress proxy did not start: %w", ctx.Err())
	}
	out, err := r.output(ctx, "container", "inspect", "--format={{json .NetworkSettings.Networks}}", o.sidecar())
	if err != nil {
		return "", err
	}
	var nets map[string]struct {
		IPAddress string `json:"IPAddress"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &nets); err != nil {
		return "", fmt.Errorf("%s container inspect: %w", r.Kind, err)
	}
	ip := nets[o.internal()].IPAddress
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("the egress proxy has no address on %s", o.internal())
	}
	return o.proxyURL(ip), nil
}

// privateCopy keeps a copy of the egress binary in dir, named by its
// content, for the proxy container to mount: on SELinux the mount is
// relabelled, which should change nodwyn's copy, not the installed file.
func privateCopy(src, dir string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("nodwyn-egress: %w", err)
	}
	sum := sha256.Sum256(data)
	dst := filepath.Join(dir, "nodwyn-egress-"+hex.EncodeToString(sum[:8]))
	if have, err := os.ReadFile(dst); err == nil && sha256.Sum256(have) == sum {
		return dst, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".nodwyn-egress-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return dst, os.Rename(tmp.Name(), dst)
}

// Stdin is the server's standard input; Stdout its standard output.
func (s *Server) Stdin() io.WriteCloser { return s.in }
func (s *Server) Stdout() io.Reader     { return s.out }

// Stderr is the end of what the server wrote to its standard error. It is
// the server's text: a caller redacts it before showing it, and never
// gives it to the model.
func (s *Server) Stderr() string { return s.stderr.String() }

// Ended says why the server's output ended: it waits a moment for the
// run command to exit and reports how.
func (s *Server) Ended() error {
	select {
	case <-s.exited:
	case <-time.After(5 * time.Second):
		return errors.New("the container is still running")
	}
	if s.waitErr != nil {
		return fmt.Errorf("the container stopped: %w", s.waitErr)
	}
	return errors.New("the container stopped")
}

// Exited is closed when the server's run command has exited.
func (s *Server) Exited() <-chan struct{} { return s.exited }

// Close stops and removes the containers and the networks. The server's
// input closes first, which ends a well-behaved server; whatever still
// runs is removed at once.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		r := s.rt
		if s.in != nil {
			s.in.Close()
		}
		if s.cmd != nil {
			select {
			case <-s.exited:
			case <-time.After(2 * time.Second):
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		names := []string{s.spec.Name}
		if s.sidecar != nil {
			names = append(names, s.spec.sidecar())
			s.sidecarIn.Close()
		}
		_, _ = r.output(ctx, r.rmArgs(names...)...)
		for _, done := range []chan struct{}{s.exitedIf(), s.sidecarDone} {
			if done == nil {
				continue
			}
			select {
			case <-done:
			case <-ctx.Done():
			}
		}
		if s.networks {
			_, _ = r.output(ctx, "network", "rm", s.spec.internal(), s.spec.outside())
		}
	})
	return nil
}

func (s *Server) exitedIf() chan struct{} {
	if s.cmd == nil {
		return nil
	}
	return s.exited
}
