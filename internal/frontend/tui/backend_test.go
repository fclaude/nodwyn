package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/frontend/rpc"
	"github.com/fclaude/nibb/internal/keychain"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

func testOptions(t *testing.T) core.Options {
	t.Helper()
	root := t.TempDir()
	return core.Options{Config: config.Default(), Repo: root, Paths: config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data")), Version: "test"}
}
func testRemote(t *testing.T, o core.Options) (*remote, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "nibb-tui-")
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
