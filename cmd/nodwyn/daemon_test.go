package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
)

func TestCLIDaemonLifecycle(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "nodwyn-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "sock")
	output, err := os.Create(filepath.Join(dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCLIHelper$", "--", "daemon", "--no-keychain", "--socket", socket)
	cmd.Env = append(os.Environ(), "NODWYN_TEST_CLI=1", quickExit, "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}()
	var conn net.Conn
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		conn, err = net.Dial("unix", socket)
		if err == nil {
			break
		}
	}
	if err != nil {
		data, _ := os.ReadFile(output.Name())
		t.Fatalf("daemon not listening: %v\n%s", err, data)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	call := func(method string, params any) json.RawMessage {
		t.Helper()
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		var reply struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := dec.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		if len(reply.Error) != 0 {
			t.Fatalf("%s: %s", method, reply.Error)
		}
		return reply.Result
	}
	call("hello", map[string]int{"protocol": 1})
	var info struct {
		ID string `json:"id"`
	}
	repo := filepath.Join(dir, "repo") // beside the config and data, as a real task folder is
	os.Mkdir(repo, 0o700)
	if err := json.Unmarshal(call("session.open", map[string]string{"repo": repo}), &info); err != nil || info.ID == "" {
		t.Fatalf("open: %s %v", info.ID, err)
	}
	conn.Close()
	// A real process signal must close logs and remove the socket, even after the
	// client that created the session has disconnected.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			data, _ := os.ReadFile(output.Name())
			t.Fatalf("daemon exit: %v\n%s", err, data)
		}
		done <- nil
	case <-ctx.Done():
		t.Fatal("daemon did not stop")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatal("socket survived shutdown", err)
	}
	paths := config.PathsUnder(filepath.Join(dir, "config", "nodwyn"), filepath.Join(dir, "data", "nodwyn"))
	session, err := core.Resume(core.Options{Config: config.Default(), Paths: paths}, info.ID)
	if err != nil {
		t.Fatal("CLI did not release session", err)
	}
	session.Close()
}

// A daemon killed outright (SIGKILL, the out-of-memory killer, a power loss)
// leaves its socket. The next daemon on that socket used to refuse it, so a
// service manager's restart failed until someone removed the file by hand.
func TestCLIDaemonRestartsAfterAKill(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "nodwyn-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "sock")
	start := func(name string) (*exec.Cmd, <-chan struct{}) {
		t.Helper()
		output, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { output.Close() })
		cmd := exec.Command(os.Args[0], "-test.run=^TestCLIHelper$", "--", "daemon", "--no-keychain", "--socket", socket)
		cmd.Env = append(os.Environ(), "NODWYN_TEST_CLI=1", quickExit, "TMPDIR="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
		cmd.Stdout = output
		cmd.Stderr = output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		var waitErr error
		go func() { waitErr = cmd.Wait(); close(exited) }()
		t.Cleanup(func() {
			cmd.Process.Kill()
			<-exited
		})
		for until := time.Now().Add(10 * time.Second); time.Now().Before(until); time.Sleep(20 * time.Millisecond) {
			select {
			case <-exited:
				data, _ := os.ReadFile(output.Name())
				t.Fatalf("%s exited before it answered: %v\n%s", name, waitErr, data)
			default:
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			client, err := rpc.Dial(ctx, socket)
			cancel()
			if err == nil {
				client.Close()
				return cmd, exited
			}
		}
		t.Fatalf("%s did not answer on %s", name, socket)
		return nil, nil
	}
	first, exited := start("first")
	if err := first.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the killed daemon did not exit")
	}
	if info, err := os.Lstat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("the killed daemon left no socket to restart over: %v %v", info, err)
	}
	start("second")
}
