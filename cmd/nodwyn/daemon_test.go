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
	cmd.Env = append(os.Environ(), "NODWYN_TEST_CLI=1", "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
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
