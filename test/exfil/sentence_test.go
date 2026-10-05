package exfil

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A key that ends a sentence is joined to its full stop in one token run,
// and L1 took the run whole, so the key matched no rule: it reached the
// model and the log, and writing it asked nothing. Here a key ends a
// sentence in a file the model reads, in the output of a command, and in
// what the model writes, through a real session: neither reaches a request
// or the log, the sentences keep their full stops around the placeholder,
// and the write is a security confirm, declined, so nothing is written.
func TestKeyEndingASentenceNeverLeaves(t *testing.T) {
	const awsKey = "AKIAABCDEFGHIJKLMNOP"
	srv := fake.NewT(t,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "1", Name: "fs-read", Args: map[string]any{"path": "notes.md"}}}},
		// The command's argv holds the canary in halves, too short to be a
		// key; only its output holds the whole canary.
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "2", Name: "shell-exec", Args: map[string]any{"cmd": "printf", "args": []any{"The API key is %s%s.\n", canary[:16], canary[16:]}}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "3", Name: "fs-write", Args: map[string]any{"path": "report.md", "content": "The bucket key is " + awsKey + ".\n"}}}},
		fake.Turn{Content: "done"},
	)
	t.Cleanup(srv.Close)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "notes.md"), []byte("The bucket key is "+awsKey+".\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "test-model"
	cfg.Loop.MaxToolCalls = 10
	var logb bytes.Buffer
	s, err := core.New(core.Options{Config: cfg, Repo: repo, Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: &logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.Send("note down the bucket key"); err != nil {
		t.Fatal(err)
	}
	// Ordinary confirms (the command) are answered yes, security ones no.
	var security []event.Confirm
	timeout := time.After(10 * time.Second)
	for idle := false; !idle; {
		select {
		case ev := <-s.Events():
			switch ev.Kind {
			case event.PromptConfirm:
				c := ev.Payload.(event.Confirm)
				answer := "y"
				if c.Prompt.Security {
					security, answer = append(security, c), "n"
				}
				if err := s.Answer(c.ID, answer); err != nil {
					t.Fatal(err)
				}
			case event.Status:
				idle = ev.Payload.(event.StatusInfo).State == "idle"
			}
		case <-timeout:
			t.Fatal("the session did not go idle")
		}
	}

	if len(srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4", len(srv.Requests))
	}
	for i, req := range srv.Requests {
		if strings.Contains(string(req.Raw), awsKey) || strings.Contains(string(req.Raw), canary) {
			t.Errorf("request %d carried a key", i)
		}
	}
	for i, want := range map[int]string{1: "The bucket key is [REDACTED aws-access-key].", 2: "The API key is [REDACTED openai-key]."} {
		if !strings.Contains(string(srv.Requests[i].Raw), want) {
			t.Errorf("request %d does not show the model %q", i, want)
		}
	}
	if len(security) != 1 || security[0].Prompt.Prim != "fs-write" {
		t.Errorf("writing the key did not ask as a security confirm: %+v", security)
	}
	if _, err := os.Stat(filepath.Join(repo, "report.md")); !os.IsNotExist(err) {
		t.Errorf("the declined write went ahead: %v", err)
	}
	if strings.Contains(logb.String(), awsKey) || strings.Contains(logb.String(), canary) {
		t.Error("a key reached the log")
	}
}
