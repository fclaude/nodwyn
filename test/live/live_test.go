//go:build live

// Package live runs nodwyn, built from this tree, against a real model
// endpoint (ADR 63). It is never part of make test, validate or promotion:
// the build tag keeps it out of every ordinary build, and the Gitea workflow
// .gitea/workflows/live.yml, or make live, runs it on purpose.
//
// The endpoint is the runner's: NODWYN_LIVE_PROVIDER, NODWYN_LIVE_URL,
// NODWYN_LIVE_MODEL and, for a keyed one, NODWYN_LIVE_KEY come from its
// environment and are never written to the repository. nodwyn itself never
// sees the endpoint's address: it talks to a recording proxy on 127.0.0.1,
// which forwards to the endpoint and keeps every request body, so a
// scenario can say what left the machine. What the suite prints, and the
// report it writes, has the address and the key replaced by placeholders.
package live

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/log"
)

// endpoint is the model the suite talks to, from the environment.
type endpoint struct {
	provider, rawURL, model, key, dialect string
	// numCtx is the context to ask for, "" for the model's own: an Ollama
	// model loaded with another num_ctx is loaded again, and one probed
	// for its largest context may not fit the server's memory.
	numCtx string
	target *url.URL
	// addrs are the addresses the endpoint's host resolves to: an error
	// from the network names the address it dialled, not the name.
	addrs []string
}

var (
	live     endpoint
	binary   string // nodwyn, built from this tree
	report   *reporter
	setupErr error
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	live = endpoint{
		provider: os.Getenv("NODWYN_LIVE_PROVIDER"),
		rawURL:   os.Getenv("NODWYN_LIVE_URL"),
		model:    os.Getenv("NODWYN_LIVE_MODEL"),
		key:      os.Getenv("NODWYN_LIVE_KEY"),
		dialect:  os.Getenv("NODWYN_LIVE_DIALECT"),
		numCtx:   os.Getenv("NODWYN_LIVE_NUM_CTX"),
	}
	dir, err := os.MkdirTemp("", "nodwyn-live-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	switch {
	case live.provider == "" || live.rawURL == "" || live.model == "":
		setupErr = fmt.Errorf("set NODWYN_LIVE_PROVIDER, NODWYN_LIVE_URL and NODWYN_LIVE_MODEL (and NODWYN_LIVE_KEY for a keyed endpoint)")
	case live.provider != "ollama" && live.provider != "openai" && live.provider != "anthropic":
		setupErr = fmt.Errorf("NODWYN_LIVE_PROVIDER is ollama, openai or anthropic, not %q", live.provider)
	default:
		if live.target, err = url.Parse(live.rawURL); err != nil || live.target.Host == "" {
			setupErr = fmt.Errorf("NODWYN_LIVE_URL is not a URL")
		}
		if live.target != nil && live.target.Hostname() != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			live.addrs, _ = net.DefaultResolver.LookupHost(ctx, live.target.Hostname())
			cancel()
		}
		if n, err := strconv.Atoi(live.numCtx); live.numCtx != "" && (err != nil || n < 1024) {
			setupErr = fmt.Errorf("NODWYN_LIVE_NUM_CTX is a context length in tokens, such as 163840")
		}
	}
	if setupErr == nil {
		binary = filepath.Join(dir, "nodwyn")
		build := exec.Command("go", "build", "-o", binary, "./cmd/nodwyn")
		build.Dir = repoRoot()
		if out, err := build.CombinedOutput(); err != nil {
			setupErr = fmt.Errorf("building nodwyn: %v\n%s", err, out)
		}
	}
	reportDir := os.Getenv("NODWYN_LIVE_REPORT")
	if reportDir == "" {
		reportDir = filepath.Join(dir, "report")
	}
	report = newReporter(reportDir)
	code := m.Run()
	if err := report.write(); err != nil {
		fmt.Fprintln(os.Stderr, "live: report:", scrub(err.Error()))
		code = 1
	}
	return code
}

func repoRoot() string {
	wd, _ := os.Getwd()
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// scrub replaces the endpoint's address, its host, the addresses the host
// resolves to and its key in s, so nothing the suite prints or keeps names
// them. The first run on the Gitea runner printed the address a refused
// connection was dialled to, which only the host's name was replaced in.
func scrub(s string) string {
	secrets := []struct{ value, as string }{
		{live.key, "[live key]"},
		{strings.TrimRight(live.rawURL, "/"), "[live url]"},
		{hostOf(live.target), "[live host]"},
	}
	for _, secret := range secrets {
		if len(secret.value) >= 4 {
			s = strings.ReplaceAll(s, secret.value, secret.as)
		}
	}
	// An address is replaced whatever its length (::1 is three letters).
	for _, addr := range live.addrs {
		s = strings.ReplaceAll(s, addr, "[live address]")
	}
	return s
}

func hostOf(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Hostname()
}

// proxy forwards nodwyn's requests to the endpoint and keeps what was sent.
type proxy struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
}

func newProxy(t *testing.T) *proxy {
	p := &proxy{}
	target := &url.URL{Scheme: live.target.Scheme, Host: live.target.Host}
	forward := httputil.NewSingleHostReverseProxy(target)
	forward.FlushInterval = -1 // streamed replies go on as they come
	director := forward.Director
	forward.Director = func(r *http.Request) {
		director(r)
		r.Host = target.Host
	}
	forward.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, scrub(err.Error()), http.StatusBadGateway)
	}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "reading the request", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		p.bodies = append(p.bodies, body)
		p.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		forward.ServeHTTP(w, r)
	}))
	t.Cleanup(p.Close)
	return p
}

// sent reports whether any request the proxy forwarded held text, and how
// many it forwarded.
func (p *proxy) sent(text string) (bool, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range p.bodies {
		if bytes.Contains(b, []byte(text)) {
			return true, len(p.bodies)
		}
	}
	return false, len(p.bodies)
}

// world is one scenario's nodwyn: its own configuration, data, keychain and
// daemon, a fixture repository, and the proxy to the endpoint.
type world struct {
	t      *testing.T
	home   string
	repo   string
	env    []string
	proxy  *proxy
	daemon *exec.Cmd
	ids    []string // the sessions the scenario made, for the report
	socket string   // the daemon's
}

// newWorld sets up a nodwyn that knows only the live endpoint (through the
// proxy) and the fixture files given, committed in a git repository.
func newWorld(t *testing.T, files map[string]string) *world {
	t.Helper()
	if setupErr != nil {
		t.Fatal(scrub(setupErr.Error()))
	}
	home := t.TempDir()
	// A socket's path is short: the runtime directory is in /tmp.
	runtime, err := os.MkdirTemp("/tmp", "nwlive-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtime) })
	w := &world{t: t, home: home, repo: filepath.Join(home, "repo"), proxy: newProxy(t), socket: filepath.Join(runtime, "nodwyn", "sock")}
	w.env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_DATA_HOME="+filepath.Join(home, "data"), "XDG_RUNTIME_DIR="+runtime,
		"NODWYN_LIVE_KEY=", "NODWYN_LIVE_URL=")
	paths := config.PathsUnder(filepath.Join(home, "config", "nodwyn"), filepath.Join(home, "data", "nodwyn"))
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	server := fmt.Sprintf("[servers.live]\nprovider = %q\nurl = %q\n", live.provider, w.proxy.URL+live.target.Path)
	if live.dialect != "" {
		server += fmt.Sprintf("dialect = %q\n", live.dialect)
	}
	if live.key != "" {
		server += "credential = \"live-key\"\n"
		store, err := keychain.Create(paths.Keychain, keychain.DefaultKDF, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Add("live-key", []byte(live.key)); err != nil {
			t.Fatal(scrub(err.Error()))
		}
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			row, err := binding.Exact("live-key", "127.0.0.1", method, config.KeyPosition(live.provider))
			if err != nil {
				t.Fatal(err)
			}
			if err := binding.AppendExact(context.Background(), paths.Bindings, row); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg := server + fmt.Sprintf("\n[control]\nserver = \"live\"\nmodel = %q\n", live.model)
	if live.numCtx != "" {
		cfg += "num_ctx = " + live.numCtx + "\n"
	}
	if err := os.WriteFile(paths.ConfigFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(w.repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(w.repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.git("init", "-q")
	w.git("add", ".")
	w.git("commit", "-qm", "fixture")
	w.startDaemon()
	return w
}

func (w *world) git(args ...string) {
	w.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = w.repo
	cmd.Env = append(w.env, "GIT_AUTHOR_NAME=live", "GIT_AUTHOR_EMAIL=live@example.invalid", "GIT_COMMITTER_NAME=live", "GIT_COMMITTER_EMAIL=live@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		w.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// startDaemon runs the world's daemon on its default socket, which line
// mode and nodwyn run then reach, and stops it when the test ends.
func (w *world) startDaemon() {
	w.t.Helper()
	cmd := exec.Command(binary, "daemon")
	cmd.Env = w.env
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		w.t.Fatal(err)
	}
	w.daemon = cmd
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	w.t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	// Ready is the socket answering a hello: nodwyn sessions reads the
	// store without the daemon, so it answered before the daemon listened.
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		client, err := rpc.Dial(ctx, w.socket)
		cancel()
		if err == nil {
			client.Close()
			return
		}
		if time.Now().After(end) {
			w.t.Fatalf("the daemon did not answer on its socket: %v", err)
		}
	}
}

// call asks the world's daemon over its socket, as the console does.
func (w *world) call(method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client, err := rpc.Dial(ctx, w.socket)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Call(ctx, method, params, result)
}

// nodwyn runs the binary in the world with input on its standard input and
// returns what it wrote, scrubbed.
func (w *world) nodwyn(limit time.Duration, input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = w.repo
	cmd.Env = w.env
	cmd.Stdin = strings.NewReader(input)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		err = fmt.Errorf("nodwyn %s gave no end in %s", args[0], limit)
	}
	return scrub(string(out)), err
}

// chat sends messages in line mode with every confirm declined, and returns
// the session's id and what line mode printed.
func (w *world) chat(limit time.Duration, extra []string, messages ...string) (string, string) {
	w.t.Helper()
	before := w.sessions()
	args := append([]string{"--no-confirm", "--no-keychain", "--repo", w.repo}, extra...)
	out, err := w.nodwyn(limit, strings.Join(append(messages, ":q"), "\n")+"\n", args...)
	if err != nil {
		w.t.Fatalf("line mode: %v\n%s", err, out)
	}
	for _, id := range w.sessions() {
		if !contains(before, id) {
			w.ids = append(w.ids, id)
			return id, out
		}
	}
	if len(extra) >= 2 && extra[0] == "--resume" {
		return extra[1], out
	}
	w.t.Fatalf("no session was made:\n%s", out)
	return "", ""
}

func (w *world) sessions() []string {
	w.t.Helper()
	out, err := w.nodwyn(30*time.Second, "", "sessions", "--ids")
	if err != nil {
		w.t.Fatalf("sessions: %v\n%s", err, out)
	}
	return strings.Fields(out)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// events reads a session's log.
func (w *world) events(id string) []log.Event {
	w.t.Helper()
	evs, err := log.ReadFile(log.SessionPath(filepath.Join(w.home, "data", "nodwyn", "sessions"), id))
	if err != nil {
		w.t.Fatalf("reading session %s: %v", id, err)
	}
	return evs
}

// logText is a session's log as written, scrubbed.
func (w *world) logText(id string) string {
	b, err := os.ReadFile(log.SessionPath(filepath.Join(w.home, "data", "nodwyn", "sessions"), id))
	if err != nil {
		w.t.Fatalf("reading session %s: %v", id, err)
	}
	return scrub(string(b))
}

// replies are the model's answers in a session, in order.
func replies(evs []log.Event) []string {
	var out []string
	for _, ev := range evs {
		var r log.LLMResp
		if ev.Kind == log.KindLLMResp && ev.Decode(&r) == nil && strings.TrimSpace(r.Content) != "" {
			out = append(out, r.Content)
		}
	}
	return out
}

// outcomes are how the session's turns ended.
func outcomes(evs []log.Event) []string {
	var out []string
	for _, ev := range evs {
		var e log.TurnEnd
		if ev.Kind == log.KindTurnEnd && ev.Decode(&e) == nil {
			out = append(out, e.Outcome)
		}
	}
	return out
}

// calls are the tools the model called, in order.
func calls(evs []log.Event) []string {
	var out []string
	for _, ev := range evs {
		var c log.ToolCall
		if ev.Kind == log.KindToolCall && ev.Decode(&c) == nil {
			out = append(out, c.Name)
		}
	}
	return out
}

// usage adds up the tokens the session's replies reported.
func usage(evs []log.Event) (prompt, output int) {
	for _, ev := range evs {
		var r log.LLMResp
		if ev.Kind == log.KindLLMResp && ev.Decode(&r) == nil {
			prompt += r.Usage.PromptTokens
			output += r.Usage.OutputTokens
		}
	}
	return prompt, output
}

// runState asks the daemon for a run's state.
func (w *world) runState(id string) (status, reason string) {
	w.t.Helper()
	out, err := w.nodwyn(30*time.Second, "", "run", "status", id)
	if err != nil {
		w.t.Fatalf("run status: %v\n%s", err, out)
	}
	// run status prints the run's state, or, from a driver that is going,
	// its id and state together.
	var r struct {
		Status, Reason string
		State          *struct{ Status, Reason string } `json:"state"`
	}
	line := out
	if i := strings.Index(line, "{"); i >= 0 {
		line = line[i:]
	}
	if err := json.Unmarshal([]byte(strings.SplitN(line, "\n", 2)[0]), &r); err != nil {
		w.t.Fatalf("run status said %q: %v", out, err)
	}
	if r.State != nil {
		return r.State.Status, r.State.Reason
	}
	if r.Status == "" {
		w.t.Fatalf("run status gave no status: %q", out)
	}
	return r.Status, r.Reason
}

// startRun submits task as a run with args and returns its session id.
func (w *world) startRun(task string, args ...string) string {
	w.t.Helper()
	file := filepath.Join(w.home, "task.txt")
	if err := os.WriteFile(file, []byte(task), 0o600); err != nil {
		w.t.Fatal(err)
	}
	out, err := w.nodwyn(60*time.Second, "", append([]string{"run", "--task", file, "--repo", w.repo}, args...)...)
	if err != nil {
		w.t.Fatalf("run: %v\n%s", err, out)
	}
	var r struct {
		ID string `json:"id"`
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if json.Unmarshal([]byte(sc.Text()), &r) == nil && r.ID != "" {
			w.ids = append(w.ids, r.ID)
			return r.ID
		}
	}
	w.t.Fatalf("run gave no id:\n%s", out)
	return ""
}

// waitRun waits until the run is no longer running, and returns its state.
func (w *world) waitRun(id string, limit time.Duration) (string, string) {
	w.t.Helper()
	for end := time.Now().Add(limit); ; time.Sleep(5 * time.Second) {
		status, reason := w.runState(id)
		if status != "running" {
			return status, reason
		}
		if time.Now().After(end) {
			_, _ = w.nodwyn(30*time.Second, "", "run", "stop", id)
			w.t.Fatalf("the run was still running after %s", limit)
		}
	}
}

// record keeps a scenario's result for the report, with the session's
// numbers, and on failure a scrubbed copy of its log.
func (w *world) record(name string) {
	start := time.Now()
	w.t.Cleanup(func() {
		r := result{Name: name, Passed: !w.t.Failed(), Skipped: w.t.Skipped(), Seconds: time.Since(start).Seconds(), Model: live.model, Provider: live.provider}
		_, r.Requests = w.proxy.sent("\x00")
		for _, id := range w.ids {
			evs, err := log.ReadFile(log.SessionPath(filepath.Join(w.home, "data", "nodwyn", "sessions"), id))
			if err != nil {
				continue
			}
			p, o := usage(evs)
			r.PromptTokens += p
			r.OutputTokens += o
			r.Outcomes = append(r.Outcomes, outcomes(evs)...)
			if w.t.Failed() {
				report.keepLog(name, id, w.logText(id))
			}
		}
		report.add(r)
	})
}
