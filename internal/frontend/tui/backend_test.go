package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fclaude/nodwyn/internal/binding"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func testOptions(t *testing.T) core.Options {
	t.Helper()
	// The task folder sits beside nodwyn's config and data, never around
	// them: a daemon refuses a root that holds its own directories.
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	return core.Options{Config: config.Default(), Repo: repo, Paths: config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data")), Version: "test"}
}
func testRemote(t *testing.T, o core.Options) (*remote, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "nodwyn-tui-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "sock")
	listener, err := rpc.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rpc.Serve(ctx, listener, o) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon shutdown hung")
		}
	})
	c, err := rpc.Dial(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return &remote{client: c}, path
}
func nextKind(t *testing.T, b backend, kind string, observed *[]rpc.ClientEvent) rpc.ClientEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		ev, err := b.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		*observed = append(*observed, ev)
		if ev.Kind == kind {
			return ev
		}
	}
}
func TestBothBackendsApproveRegisterAndCancel(t *testing.T) {
	for _, mode := range []string{"local", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			o := testOptions(t)
			model := fake.New(fake.Turn{Content: "Hello from the model"})
			defer model.Close()
			o.Provider = llm.NewOllama(model.URL)
			store, err := keychain.Create(filepath.Join(o.Repo, "test.keychain"), keychain.FastKDF, []byte("test-passphrase"))
			if err != nil {
				t.Fatal(err)
			}
			o.Keychain = store
			var b backend
			if mode == "local" {
				b = newLocal(o)
			} else {
				b, _ = testRemote(t, o)
			}
			defer b.Close()
			ctx := context.Background()
			snap, err := b.Attach(ctx, o.Repo, "")
			if err != nil {
				t.Fatal(err)
			}
			var events []rpc.ClientEvent
			if err := b.Send(ctx, snap.ID, `(fs-write "approved.txt" "yes")`, true); err != nil {
				t.Fatal(err)
			}
			ev := nextKind(t, b, event.PromptConfirm, &events)
			p, _ := decode[event.Confirm](ev.Payload)
			if err := b.Answer(ctx, snap.ID, p.ID, "y"); err != nil {
				t.Fatal(err)
			}
			nextKind(t, b, event.Status, &events)
			if raw, err := os.ReadFile(filepath.Join(o.Repo, "approved.txt")); err != nil || string(raw) != "yes" {
				t.Fatal("approved write failed", err)
			}
			if err := b.Send(ctx, snap.ID, `(credential-register "test-service")`, true); err != nil {
				t.Fatal(err)
			}
			ev = nextKind(t, b, event.PromptSecret, &events)
			secret, _ := decode[event.SecretPrompt](ev.Payload)
			const canary = "tui-canary-3e7ac53f"
			if err := b.Secret(ctx, snap.ID, secret.ID, []byte(canary)); err != nil {
				t.Fatal(err)
			}
			nextKind(t, b, event.Status, &events)
			if store.Count() != 1 {
				t.Fatal("credential not registered")
			}
			for _, e := range events {
				if strings.Contains(string(e.Payload), canary) {
					t.Fatal("credential entered events")
				}
			}
			if err := b.Send(ctx, snap.ID, `(let loop () (loop))`, true); err != nil {
				t.Fatal(err)
			}
			nextKind(t, b, string(log.KindEvalReq), &events)
			if err := b.Abort(ctx, snap.ID); err != nil {
				t.Fatal(err)
			}
			ev = nextKind(t, b, string(log.KindEvalResult), &events)
			result, _ := decode[log.EvalResult](ev.Payload)
			if result.Error == "" {
				t.Fatal("eval cancellation did not propagate")
			}
			nextKind(t, b, event.Status, &events)
			if err := b.Send(ctx, snap.ID, "Hello", false); err != nil {
				t.Fatal(err)
			}
			ev = nextKind(t, b, event.StreamText, &events)
			text, _ := decode[event.Text](ev.Payload)
			if text.Text != "Hello from the model" {
				t.Fatal("model text not delivered")
			}
			for {
				ev = nextKind(t, b, event.Status, &events)
				state, _ := decode[event.StatusInfo](ev.Payload)
				if state.State == "idle" {
					break
				}
			}
		})
	}
}
func TestRemoteReconnectRecoversPendingPromptAndHistory(t *testing.T) {
	o := testOptions(t)
	first, path := testRemote(t, o)
	ctx := context.Background()
	snap, err := first.Attach(ctx, o.Repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Send(ctx, snap.ID, `(fs-write "declined.txt" "no")`, true); err != nil {
		t.Fatal(err)
	}
	var seen []rpc.ClientEvent
	ev := nextKind(t, first, event.PromptConfirm, &seen)
	p, _ := decode[event.Confirm](ev.Payload)
	first.Close()
	c, err := rpc.Dial(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	second := &remote{c}
	defer second.Close()
	restored, err := second.Attach(ctx, o.Repo, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.Busy || len(restored.Pending) != 1 || len(restored.History) == 0 {
		t.Fatal("incomplete catch-up")
	}
	pending, _ := decode[event.Confirm](restored.Pending[0].Payload)
	if pending.ID != p.ID {
		t.Fatal("reconnect replaced prompt identity")
	}
	if err := second.Answer(ctx, snap.ID, p.ID, "n"); err != nil {
		t.Fatal(err)
	}
	nextKind(t, second, event.Status, &seen)
	if _, err := os.Stat(filepath.Join(o.Repo, "declined.txt")); !os.IsNotExist(err) {
		t.Fatal("denied write ran")
	}
}
func wire(kind string, payload any, seq uint64) rpc.ClientEvent {
	raw, _ := json.Marshal(payload)
	return rpc.ClientEvent{ID: "session", Kind: kind, Seq: seq, Payload: raw}
}

func TestLocalCloseDrainsBackpressureAndReleasesLog(t *testing.T) {
	o := testOptions(t)
	b := newLocal(o)
	ctx := context.Background()
	s, err := b.Attach(ctx, o.Repo, "")
	if err != nil {
		t.Fatal(err)
	}
	// More events than either frontend queue; Close must drain while canceling.
	if err := b.Send(ctx, s.ID, `(let loop ((n 0)) (fs-list ".") (loop (+ n 1)))`, true); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { b.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("close deadlocked behind event backpressure")
	}
	resumed, err := core.Resume(o, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed.Close()
}

func TestAbortThroughTUIBackendKillsShellChildren(t *testing.T) {
	for _, mode := range []string{"local", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			o := testOptions(t)
			var b backend
			if mode == "local" {
				b = newLocal(o)
			} else {
				b, _ = testRemote(t, o)
			}
			defer b.Close()
			ctx := context.Background()
			s, err := b.Attach(ctx, o.Repo, "")
			if err != nil {
				t.Fatal(err)
			}
			source := `(shell-exec "sh" '("-c" "sleep 30 & echo $! > child.pid; wait"))`
			if err := b.Send(ctx, s.ID, source, true); err != nil {
				t.Fatal(err)
			}
			var events []rpc.ClientEvent
			e := nextKind(t, b, event.PromptConfirm, &events)
			p, _ := decode[event.Confirm](e.Payload)
			if err := b.Answer(ctx, s.ID, p.ID, "y"); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			var raw []byte
			for time.Now().Before(deadline) {
				raw, _ = os.ReadFile(filepath.Join(o.Repo, "child.pid"))
				if len(raw) > 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatal("shell did not start child", err)
			}
			// Kill only this test's child if an assertion fails, so the test cannot leak it.
			alive := true
			defer func() {
				if alive {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}()
			if err := b.Abort(ctx, s.ID); err != nil {
				t.Fatal(err)
			}
			nextKind(t, b, string(log.KindEvalResult), &events)
			deadline = time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
					alive = false
					return
				}
				// A killed child may await reaping by the container's init; it is no longer running.
				zombie := false
				if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
					_, tail, ok := strings.Cut(string(stat), ") ")
					zombie = ok && strings.HasPrefix(tail, "Z ")
				} else if output, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output(); err == nil {
					zombie = strings.HasPrefix(strings.TrimSpace(string(output)), "Z")
				}
				if zombie {
					alive = false
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("shell child survived cancellation")
		})
	}
}

func TestBothBackendsDeliverCredentialEgressScope(t *testing.T) {
	for _, mode := range []string{"local", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			o := testOptions(t)
			o.Config.Policy.Unbound = "confirm"
			store, err := keychain.Create(filepath.Join(o.Repo, "test.keychain"), keychain.FastKDF, []byte("test-passphrase"))
			if err != nil {
				t.Fatal(err)
			}
			o.Keychain = store
			var b backend
			if mode == "local" {
				b = newLocal(o)
			} else {
				b, _ = testRemote(t, o)
			}
			defer b.Close()
			ctx := context.Background()
			snap, err := b.Attach(ctx, o.Repo, "")
			if err != nil {
				t.Fatal(err)
			}
			source := `(let ((key (credential-register "scope-service"))) (http-request key "https://api.example.test/v1" "POST" "header:Authorization:Bearer" '() ""))`
			if err := b.Send(ctx, snap.ID, source, true); err != nil {
				t.Fatal(err)
			}
			var events []rpc.ClientEvent
			secretEvent := nextKind(t, b, event.PromptSecret, &events)
			secret, err := decode[event.SecretPrompt](secretEvent.Payload)
			if err != nil {
				t.Fatal(err)
			}
			const canary = "scope-canary-918b7e"
			if err := b.Secret(ctx, snap.ID, secret.ID, []byte(canary)); err != nil {
				t.Fatal(err)
			}
			ev := nextKind(t, b, event.PromptConfirm, &events)
			prompt, err := decode[event.Confirm](ev.Payload)
			if err != nil {
				t.Fatal(err)
			}
			scope := prompt.Prompt.Egress
			if scope == nil || scope.Class != "scope-service" || scope.Host != "api.example.test" || scope.Method != "POST" || scope.Position != "header:Authorization:Bearer" {
				t.Fatalf("scope = %+v", scope)
			}
			if err := b.Grant(ctx, snap.ID, prompt.ID, "session"); err == nil {
				t.Fatal("egress granted standing approval")
			}
			if err := b.Answer(ctx, snap.ID, prompt.ID, "n"); err != nil {
				t.Fatal(err)
			}
			nextKind(t, b, event.Status, &events)
			policy, err := b.Policy(ctx, snap.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(policy.Bindings) != 0 {
				t.Fatal("confirmation modified bindings")
			}
			for _, ev := range events {
				if strings.Contains(string(ev.Payload), canary) {
					t.Fatal("secret entered frontend events")
				}
			}
		})
	}
}

func TestBothBackendsPersistExactBindingAndReloadPolicy(t *testing.T) {
	for _, mode := range []string{"local", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			const canary = "binding-canary-931c5f"
			service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+canary {
					t.Error("wrong injected credential")
				}
				requests.Add(1)
				fmt.Fprint(w, "ok")
			}))
			defer service.Close()
			o := testOptions(t)
			o.Config.Policy.Unbound = "confirm"
			store, err := keychain.Create(filepath.Join(o.Repo, "test.keychain"), keychain.FastKDF, []byte("test"))
			if err != nil {
				t.Fatal(err)
			}
			o.Keychain = store
			var b backend
			socket := ""
			if mode == "local" {
				b = newLocal(o)
			} else {
				b, socket = testRemote(t, o)
			}
			defer func() { b.Close() }()
			ctx := context.Background()
			snap, err := b.Attach(ctx, o.Repo, "")
			if err != nil {
				t.Fatal(err)
			}
			call := fmt.Sprintf(`(http-request cred %q "POST" "header:Authorization:Bearer" '() "")`, service.URL)
			if err := b.Send(ctx, snap.ID, `(begin (define cred (credential-register "service")) `+call+`)`, true); err != nil {
				t.Fatal(err)
			}
			var seen []rpc.ClientEvent
			secret, err := decode[event.SecretPrompt](nextKind(t, b, event.PromptSecret, &seen).Payload)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Secret(ctx, snap.ID, secret.ID, []byte(canary)); err != nil {
				t.Fatal(err)
			}
			prompt, err := decode[event.Confirm](nextKind(t, b, event.PromptConfirm, &seen).Payload)
			if err != nil || prompt.Prompt.Binding == nil {
				t.Fatal("missing binding proposal", err)
			}
			if mode == "rpc" {
				b.Close()
				client, err := rpc.Dial(ctx, socket)
				if err != nil {
					t.Fatal(err)
				}
				b = &remote{client: client}
				restored, err := b.Attach(ctx, o.Repo, snap.ID)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, ev := range restored.Pending {
					p, _ := decode[event.Confirm](ev.Payload)
					if p.ID == prompt.ID && p.Prompt.Binding != nil {
						found = true
					}
				}
				if !found {
					t.Fatal("reconnect lost binding proposal")
				}
			}
			if remote, ok := b.(*remote); ok {
				if err := remote.client.Call(ctx, "session.binding.create", map[string]any{"id": snap.ID, "prompt_id": prompt.ID, "dest": "evil.test"}, nil); err == nil {
					t.Fatal("RPC accepted caller-selected target")
				}
			}
			// A policy edit while awaiting consent must not be overwritten or permit
			// the waiting request if persistence fails.
			if err := os.MkdirAll(filepath.Dir(o.Paths.Bindings), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(o.Paths.Bindings, []byte("broken = true"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := b.Bind(ctx, snap.ID, prompt.ID); err != nil {
				t.Fatal(err)
			}
			nextKind(t, b, event.Status, &seen)
			if requests.Load() != 0 {
				t.Fatal("failed persistence allowed transport")
			}
			failed := false
			for _, ev := range seen {
				if ev.Kind == string(log.KindBindingChange) {
					p, _ := decode[log.BindingChange](ev.Payload)
					if p.State == "failed" && p.PromptID == prompt.ID {
						failed = true
					}
				}
			}
			if !failed {
				t.Fatal("missing failed persistence audit")
			}
			raw, _ := os.ReadFile(o.Paths.Bindings)
			if string(raw) != "broken = true" {
				t.Fatal("invalid edit overwritten")
			}
			if err := os.Remove(o.Paths.Bindings); err != nil {
				t.Fatal(err)
			}
			if err := b.Send(ctx, snap.ID, call, true); err != nil {
				t.Fatal(err)
			}
			prompt, _ = decode[event.Confirm](nextKind(t, b, event.PromptConfirm, &seen).Payload)
			// Explicit operation only: a string reply must never create a binding.
			if b.Answer(ctx, snap.ID, prompt.ID, "b") == nil {
				t.Fatal("ordinary answer accepted persistent grant")
			}
			if err := b.Bind(ctx, snap.ID, prompt.ID); err != nil {
				t.Fatal(err)
			}
			nextKind(t, b, event.Status, &seen)
			if requests.Load() != 1 {
				t.Fatal("approved request count", requests.Load())
			}
			table, err := binding.Load(o.Paths.Bindings)
			if err != nil || len(table.Bind) != 1 || table.Bind[0].Dest != "127.0.0.1" {
				t.Fatal(table, err)
			}
			for _, state := range []string{"requested", "applied"} {
				found := false
				for _, ev := range seen {
					if ev.Kind != string(log.KindBindingChange) {
						continue
					}
					p, _ := decode[log.BindingChange](ev.Payload)
					if p.State == state && p.PromptID == prompt.ID && p.Actor != "" {
						found = true
					}
				}
				if !found {
					t.Fatal("missing attributed outcome", state)
				}
			}
			if b.Bind(ctx, snap.ID, prompt.ID) == nil {
				t.Fatal("reused answered prompt")
			}
			// The next call passes without a confirmation in the same session.
			seen = nil
			if err := b.Send(ctx, snap.ID, call, true); err != nil {
				t.Fatal(err)
			}
			nextKind(t, b, event.Status, &seen)
			if requests.Load() != 2 {
				t.Fatal("binding did not take effect", requests.Load())
			}
			for _, ev := range seen {
				if ev.Kind == event.PromptConfirm {
					t.Fatal("bound call prompted")
				}
			}
			// A malformed edit must deny, even under the opt-in confirm policy.
			if err := os.WriteFile(o.Paths.Bindings, []byte("unknown = true"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := b.Send(ctx, snap.ID, call, true); err != nil {
				t.Fatal(err)
			}
			nextKind(t, b, event.Status, &seen)
			if requests.Load() != 2 {
				t.Fatal("stale policy authorized after invalid edit")
			}
			// Deleting the policy removes authority before the next egress.
			if err := os.Remove(o.Paths.Bindings); err != nil {
				t.Fatal(err)
			}
			if err := b.Send(ctx, snap.ID, call, true); err != nil {
				t.Fatal(err)
			}
			prompt, _ = decode[event.Confirm](nextKind(t, b, event.PromptConfirm, &seen).Payload)
			if err := b.Abort(ctx, snap.ID); err != nil {
				t.Fatal(err)
			}
			if b.Bind(ctx, snap.ID, prompt.ID) == nil {
				t.Fatal("canceled prompt granted binding")
			}
			nextKind(t, b, event.Status, &seen)
			if _, err := os.Stat(o.Paths.Bindings); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("canceled grant persisted")
			}
			history, err := b.History(ctx, snap.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, ev := range history {
				if strings.Contains(string(ev.Payload), canary) {
					t.Fatal("canary in durable history")
				}
			}
		})
	}
}

func TestBothBackendsStandingAndTurnApprovalFlows(t *testing.T) {
	for _, mode := range []string{"local", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			o := testOptions(t)
			for _, dir := range []string{"notes", "drafts", ".nodwyn"} {
				if err := os.MkdirAll(filepath.Join(o.Repo, dir), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			var b backend
			if mode == "local" {
				b = newLocal(o)
			} else {
				b, _ = testRemote(t, o)
			}
			defer b.Close()
			ctx := context.Background()
			snap, err := b.Attach(ctx, o.Repo, "")
			if err != nil {
				t.Fatal(err)
			}
			var seen []rpc.ClientEvent
			send := func(source string) {
				t.Helper()
				if err := b.Send(ctx, snap.ID, source, true); err != nil {
					t.Fatal(err)
				}
			}
			prompt := func() event.Confirm {
				t.Helper()
				p, err := decode[event.Confirm](nextKind(t, b, event.PromptConfirm, &seen).Payload)
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			send(`(fs-write "notes/a" "one")`)
			p := prompt()
			if err := b.Grant(ctx, snap.ID, p.ID, "session"); err != nil {
				t.Fatal(err)
			}
			nextKind(t, b, event.Status, &seen)
			send(`(fs-write "notes/b" "two")`)
			nextKind(t, b, event.Status, &seen)
			send(`(begin (fs-write "drafts/a" "three") (fs-write "outside.txt" "four"))`)
			p = prompt()
			if err := b.Grant(ctx, snap.ID, p.ID, "turn"); err != nil {
				t.Fatal(err)
			}
			nextKind(t, b, event.Status, &seen)
			for _, path := range []string{"notes/a", "notes/b", "drafts/a", "outside.txt"} {
				if _, err := os.Stat(filepath.Join(o.Repo, path)); err != nil {
					t.Fatal("granted write missing", path, err)
				}
			}
			// Turn trust expires; session directory permission remains narrow.
			send(`(fs-write "next-turn.txt" "deny")`)
			p = prompt()
			if err := b.Answer(ctx, snap.ID, p.ID, "n"); err != nil {
				t.Fatal(err)
			}
			nextKind(t, b, event.Status, &seen)
			if _, err := os.Stat(filepath.Join(o.Repo, "next-turn.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("turn authority persisted")
			}
			// Protected paths still require one-time consent.
			send(`(fs-write ".nodwyn/init.scm" "; deny")`)
			p = prompt()
			for _, scope := range []string{"session", "turn"} {
				if b.Grant(ctx, snap.ID, p.ID, scope) == nil {
					t.Fatal("protected path widened", scope)
				}
			}
			if err := b.Answer(ctx, snap.ID, p.ID, "n"); err != nil {
				t.Fatal(err)
			}
			nextKind(t, b, event.Status, &seen)
			policy, err := b.Policy(ctx, snap.ID)
			if err != nil || policy.TurnWrites || len(policy.Writes) != 1 || policy.Writes[0] != "notes/" {
				t.Fatal(policy, err)
			}
			grants := 0
			for _, ev := range seen {
				if ev.Kind == string(log.KindApprovalChange) {
					p, _ := decode[log.ApprovalChange](ev.Payload)
					if p.Actor == "" {
						t.Fatal("unattributed approval")
					}
					grants++
				}
			}
			if grants != 2 {
				t.Fatal("unexpected approval records", grants)
			}
		})
	}
}
