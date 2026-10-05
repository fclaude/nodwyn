// Command demo runs the browser console against synthetic work: a fake model
// that looks around a scratch folder, proposes an edit, runs a command and
// answers, a daemon on temporary directories, and the console bridge. It is
// for looking at the console and for browser acceptance checks, never for
// real work: every directory is temporary and the model is scripted. A
// contained MCP server added from Connections runs in a fake container
// runtime (test/fakes/container), which is this program run again: nothing
// is pulled or contained, and IMAGE is an image it has to pull first.
//
//	go run ./test/browser/demo            # prints the URL (with a launch ticket)
//	go run ./test/browser/demo -seed      # also starts tasks so every view has content
//	go run ./test/browser/demo -docs      # a Google credential, and a fake Drive to upload to
//	go run ./test/browser/demo -oauth-only  # only a fake OAuth2 provider, for Connections › Credentials
//	go run ./test/browser/demo -chat-only   # only a fake chat-completions provider, for Settings › Models
//
// The scripted model waits 400 ms before each reply, so a person watching
// sees the work go step by step; the browser checks pass -pace=0, since none
// of them looks at a step in between.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/container"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	webui "github.com/fclaude/nodwyn/internal/frontend/web"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	fakechat "github.com/fclaude/nodwyn/test/fakes/chat"
	fakecontainer "github.com/fclaude/nodwyn/test/fakes/container"
	fakedrive "github.com/fclaude/nodwyn/test/fakes/drive"
	fakemcp "github.com/fclaude/nodwyn/test/fakes/mcp"
	fakeoauth "github.com/fclaude/nodwyn/test/fakes/oauth"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// pace is how long the scripted model waits before each reply.
var pace time.Duration

// chatKey is the key the demo's chat-completions provider wants: made up, and
// worth nothing anywhere else.
const chatKey = "sk-demo-key-0123456789"

// notesImage is the image of the demo's contained server.
const notesImage = "ghcr.io/example/mcp-notes@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func main() {
	// Run again by the fake container runtime, this program is the runtime.
	fakecontainer.Main(notesTools()...)
	seed := flag.Bool("seed", false, "start demo tasks so every view has content")
	port := flag.Int("port", 0, "loopback port for the console (0 picks one)")
	dir := flag.String("dir", "", "reuse this root from a previous run, as an app restart reuses its setup")
	modelOnly := flag.Bool("model-only", false, "run only the scripted model server and print its URL")
	docs := flag.Bool("docs", false, "keep a Google credential in a throwaway keychain, bound to a fake Drive that takes uploads")
	oauthOnly := flag.Bool("oauth-only", false, "run only a fake OAuth2 provider (client id demo-client) and print its endpoints")
	chatOnly := flag.Bool("chat-only", false, "run only a fake chat-completions provider and print its address and key")
	flag.DurationVar(&pace, "pace", 400*time.Millisecond, "how long the scripted model waits before each reply")
	flag.Parse()
	if *oauthOnly {
		provider := fakeoauth.New("demo-client")
		fmt.Printf("OAUTH %s %s %s\n", provider.AuthURL(), provider.TokenURL(), provider.RevokeURL())
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		<-ctx.Done()
		stop()
		provider.Close()
		return
	}
	if *chatOnly {
		// Two models: one takes an effort and one does not, and the provider
		// refuses what it should (a wrong key, a model it lacks, an effort a
		// model does not take). /_fake/requests shows what it was sent.
		provider := fakechat.New(chatKey,
			fakechat.Model{ID: "reasoner", Context: 200000, Efforts: []string{"low", "medium", "high"}},
			fakechat.Model{ID: "fast", Context: 200000},
		)
		fmt.Printf("CHAT %s KEY %s\n", provider.V1(), chatKey)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		<-ctx.Done()
		stop()
		provider.Close()
		return
	}
	if *modelOnly {
		model := fake.NewFunc(respond)
		fmt.Printf("MODEL %s\n", model.URL)
		model.Config.Handler = logged(model.Config.Handler)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		<-ctx.Done()
		stop()
		model.Close()
		return
	}
	if err := run(*seed, *port, *docs, *dir); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

func run(seed bool, port int, docs bool, given string) error {
	root, kept := given, given != ""
	if !kept {
		var err error
		if root, err = os.MkdirTemp("/tmp", "nodwyn-demo-"); err != nil {
			return err
		}
	}
	defer func() {
		// A root the run made itself is throwaway; one given with -dir
		// belongs to whoever passed it (the checks reuse one to restart the
		// app), so this run leaves it in place.
		if !kept {
			os.RemoveAll(root)
		}
	}()
	model := fake.NewFunc(respond)
	defer model.Close()
	// "Tidy" ends on a provider's rate limit: retry it quickly, so the
	// turn stops in a second rather than six.
	llm.RetryDelay = 100 * time.Millisecond
	model.SetCapabilities("completion", "tools", "vision") // images may be attached
	chat := chatServer()
	defer chat.Close()

	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	cfg := config.Default()
	cfg.Control = config.Model{URL: model.URL, Model: "demo"}
	cfg.Jev.Content = config.JevContentNone
	runtime, err := fakecontainer.Install(filepath.Join(root, "runtime"), "podman")
	if err != nil {
		return err
	}
	defer runtime.StopAll()
	cfg.MCP.Egress = filepath.Join(root, "runtime", "nodwyn-egress")
	if err := os.WriteFile(cfg.MCP.Egress, []byte("#!/bin/false\n"), 0o755); err != nil {
		return err
	}
	var store *keychain.Store
	if docs {
		// A throwaway keychain holding a made-up Google token, bound to the
		// fake Drive's host: what nodwyn credential add --oauth2 google and
		// the binding it proposes would leave, with nothing real in it.
		drive := fakedrive.New()
		defer drive.Close()
		if store, err = keychain.Create(filepath.Join(root, "keychain"), keychain.FastKDF, []byte("demo")); err != nil {
			return err
		}
		if _, err := store.Add("google", []byte("ya29.demo-not-a-token")); err != nil {
			return err
		}
		row, err := binding.Exact("google", "127.0.0.1", "POST", "header:Authorization:Bearer")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
			return err
		}
		if err := binding.AppendExact(context.Background(), paths.Bindings, row); err != nil {
			return err
		}
		cfg.Docs.GoogleUploadURL = drive.UploadURL()
	}
	// The demo's configuration is in its config.toml, as a daemon's is, so
	// Settings changes it there as it would anyone's.
	raw, _, err := config.SetControl(nil, cfg.Control)
	if err != nil {
		return err
	}
	if raw, cfg, err = config.Edit(raw,
		config.Change{Table: "jev", Key: "content", Value: cfg.Jev.Content},
		config.Change{Table: "mcp", Key: "egress", Value: cfg.MCP.Egress},
		config.Change{Table: "docs", Key: "google_upload_url", Value: cfg.Docs.GoogleUploadURL}); err != nil {
		return err
	}
	if err := config.WriteFile(paths.ConfigFile, raw); err != nil {
		return err
	}
	options := core.Options{Config: cfg, Paths: paths, Version: "demo", Keychain: store, Containers: &container.Runtime{Kind: runtime.Kind, Path: runtime.Path}}

	if err := cfg.Validate(); err != nil {
		return err
	}
	socket := filepath.Join(root, "run", "sock")
	if kept {
		// Listen refuses an existing socket, even a stale one, and a run on
		// a given root starts after the run that made it (the checks wait
		// for the old demo to exit first), so this run removes its socket.
		os.Remove(socket)
	}
	listener, err := rpc.Listen(socket)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	daemonDone := make(chan error, 1)
	go func() { daemonDone <- rpc.Serve(ctx, listener, options) }()

	// A git repository with one commit, for tasks in worktrees of their
	// own (ADR 34); the other folders stay plain folders, as the checks
	// that look at them expect.
	project := filepath.Join(root, "work", "project")
	repos := map[string]string{"console": filepath.Join(root, "work", "console")}
	// A root reused for a restart keeps the world a previous run made:
	// rewriting the folders would undo what its tasks did to them.
	if _, err := os.Stat(filepath.Join(root, "work", "console")); err != nil {
		for _, name := range []string{"console", "receipts"} {
			dir := filepath.Join(root, "work", name)
			repos[name] = dir
			if err := writeRepo(dir, name); err != nil {
				return err
			}
		}
		if err := writeGitRepo(project); err != nil {
			return err
		}
	}
	if seed {
		if err := seedTasks(ctx, socket, repos); err != nil {
			return err
		}
	}

	code, err := webui.PairingCode()
	if err != nil {
		return err
	}
	web, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	console := webui.New(webui.Options{Socket: socket, PairingCode: code})
	ticket, err := console.Ticket()
	if err != nil {
		return err
	}
	url := "http://" + web.Addr().String()
	fmt.Printf("URL %s/#ticket=%s\nCODE %s\nREPO %s\nMCP %s\nIMAGE %s\nGIT %s\n", url, ticket, code, repos["console"], chat.URL, notesImage, project)
	go func() {
		// More tickets for automated checks: one per line on stdin.
		buf := make([]byte, 1)
		for {
			if _, err := os.Stdin.Read(buf); err != nil {
				return
			}
			if buf[0] == '\n' {
				if t, err := console.Ticket(); err == nil {
					fmt.Printf("TICKET %s\n", t)
				}
			}
		}
	}()
	err = console.Serve(ctx, web)
	stop()
	return errorsJoin(err, <-daemonDone)
}

// logged prints each request's path, so a check can see a page reach the
// model through the daemon.
func logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("REQUEST %s %s\n", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func errorsJoin(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func writeRepo(dir, name string) error {
	files := map[string]string{
		"README.md": "# " + name + "\n\nA scratch folder for the console demo.\n",
		"app.js": `function sessionRow(session) {
  const row = node("button", "", "session");
  row.append(node("span", session.first, "title"));
  row.append(node("span", session.repo, "meta"));
  return row;
}
`,
		"notes/todo.md": "- tell sessions apart\n- check phone width\n",
	}
	if name == "console" {
		// The demo has no judge, so its coding task would get no plan: plan
		// it anyway, without exploring first, so the plan panel has a plan
		// and the model's step marks to show. A turn that did not finish
		// leaves its plan to the next, as the default gate does.
		files["status.txt"] = "Week of 28 September: cowork workspaces, attachments that read PDFs, phone approvals.\n"
		// A protected path, so an edit of it asks even though writes in the
		// folder go ahead: "Quiet the build" edits it twice in one call.
		files["Makefile"] = "build:\n\tgo build ./...\n\ntest:\n\tgo test ./...\n"
		files[".nodwyn/init.scm"] = "(define plan-explore-rounds 0)\n(define (plan-gate request) (cond ((plan-recorded?) #f) ((plan-left) => plan-carry) (else (plan-best #f request \"plan\"))))\n"
	}
	for rel, body := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeGitRepo makes dir a git repository on main with the demo's files
// committed, by an identity of its own and unsigned, whatever the person's
// git configuration says.
func writeGitRepo(dir string) error {
	if err := writeRepo(dir, "project"); err != nil {
		return err
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "commit.gpgsign=false", "commit", "-q", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=demo", "GIT_AUTHOR_EMAIL=demo@nodwyn.invalid", "GIT_COMMITTER_NAME=demo", "GIT_COMMITTER_EMAIL=demo@nodwyn.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("demo: git %v: %v: %s", args, err, out)
		}
	}
	return nil
}

// seedTasks starts one task that finishes and one that stops at an
// approval, and returns once both have: what the URL then opens has
// settled, so a check looks at it without waiting for a guess at how long
// the model takes. The finished one goes first so that the order is
// always the same.
func seedTasks(ctx context.Context, socket string, repos map[string]string) error {
	dial, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, err := rpc.Dial(dial, socket)
	if err != nil {
		return err
	}
	defer client.Close()
	start := func(repo, text string) (string, error) {
		var info struct {
			ID string `json:"id"`
		}
		if err := client.Call(ctx, "session.open", map[string]any{"repo": repo}, &info); err != nil {
			return "", err
		}
		return info.ID, client.Call(ctx, "session.send", map[string]any{"id": info.ID, "text": text}, nil)
	}
	// until asks the daemon every 20 ms whether the task has got there;
	// session.send returns with the task busy, so "not busy" is its end.
	until := func(what string, there func() (bool, error)) error {
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			if ok, err := there(); ok || err != nil {
				return err
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("demo: the seeded task never %s", what)
			}
		}
	}
	id, err := start(repos["receipts"], "Summarize what is in this folder")
	if err != nil {
		return err
	}
	if err := until("finished", func() (bool, error) {
		var list []struct {
			ID   string `json:"id"`
			Busy bool   `json:"busy"`
		}
		err := client.Call(ctx, "session.list", map[string]any{}, &list)
		for _, s := range list {
			if s.ID == id {
				return !s.Busy, err
			}
		}
		return false, err
	}); err != nil {
		return err
	}
	if id, err = start(repos["console"], "The session list shows each session's first message, cut short, so two sessions that start the same way look identical. Make them easy to tell apart. Keep the change inside the web console."); err != nil {
		return err
	}
	return until("asked", func() (bool, error) {
		var asking []struct {
			ID      string            `json:"id"`
			Pending []json.RawMessage `json:"pending"`
		}
		err := client.Call(ctx, "attention.list", map[string]any{}, &asking)
		for _, s := range asking {
			if s.ID == id && len(s.Pending) > 0 {
				return true, err
			}
		}
		return false, err
	})
}

// chatServer is a fake team-chat MCP server: search and list read, posting
// a message is a tool nobody declared.
func chatServer() *fakemcp.Server {
	return fakemcp.New(
		fakemcp.Tool{Name: "search_messages", Description: "Search messages in the team's channels.", Schema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []any{"query"}},
			Reply: func(args map[string]any) (string, bool) {
				return "3 messages: 'cowork workspaces landed', 'attachments read PDFs', 'phone approvals work'", false
			}},
		fakemcp.Tool{Name: "list_channels", Description: "List channels.", Reply: func(map[string]any) (string, bool) { return "#eng-status, #random", false }},
		fakemcp.Tool{Name: "post_message", Description: "Post a message to a channel.", Schema: map[string]any{"type": "object", "properties": map[string]any{"channel": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"}}, "required": []any{"channel", "text"}},
			Reply: func(map[string]any) (string, bool) { return "posted", false }},
	)
}

// notesTools are what the demo's contained server offers: a search that
// reads, and a note it writes, which nobody declared.
func notesTools() []fakemcp.Tool {
	return []fakemcp.Tool{
		{Name: "search_notes", Description: "Search the team's notes.", Schema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []any{"query"}},
			Reply: func(map[string]any) (string, bool) { return "2 notes: 'weekly status', 'phone approvals'", false }},
		{Name: "write_note", Description: "Write a note.", Schema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []any{"text"}},
			Reply: func(map[string]any) (string, bool) { return "written", false }},
	}
}

// planStep is the model marking a step of its plan.
func planStep(id string, step int, state, note string) fake.ToolCall {
	return fake.ToolCall{ID: id, Name: "plan-step", Args: map[string]any{"step": step, "state": state, "note": note}}
}

// respond is the scripted model. A request offering no tools is the planner;
// otherwise the reply depends on how many tool rounds this ask has had.
func respond(req fake.Request) fake.Turn {
	// Recipe v15 (ADR 9 amended 2026-10-04) carries the person's request
	// and the recorded plan as user messages under their headers
	// (prompts.scm's task-header and plan-header): the ask is the text
	// under the task header, and the plan message continues the request it
	// plans, so it says nothing new to this script.
	const (
		taskHeader = "Current task (the user's latest request):\n"
		planHeader = "Plan for the current task"
	)
	var last string
	rounds, images := 0, 0
	for _, raw := range req.Messages {
		var m struct {
			Role      string            `json:"role"`
			Content   string            `json:"content"`
			ToolCalls []json.RawMessage `json:"tool_calls"`
			Images    []string          `json:"images"`
		}
		_ = json.Unmarshal(raw, &m)
		images += len(m.Images)
		switch {
		case m.Role == "user" && strings.HasPrefix(m.Content, taskHeader):
			last, rounds = m.Content[len(taskHeader):], 0
		case m.Role == "user" && strings.HasPrefix(m.Content, planHeader):
			// The plan the turn is working: last and rounds stay.
		case m.Role == "user" && !strings.HasPrefix(m.Content, "["):
			last, rounds = m.Content, 0
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			rounds++
		}
	}
	time.Sleep(pace)
	if len(req.Tools) == 0 {
		// The plan names its change, its test, its docs and its check, as
		// the planner is asked to (prompts.scm, plan-shape).
		return fake.Turn{Content: "1. Change: app.js sessionRow — under each title, show when the session last changed and whether it's running, and its id on hover.\n" +
			"2. Test: app.test.js \"rows tell sessions apart\" — two sessions that start alike get different rows; fails today: a row shows only the title and the repository.\n" +
			"3. Docs: notes/todo.md — tick off \"tell sessions apart\".\n" +
			"4. Check: ls -la, then the list at phone width.", Chunk: 24}
	}
	if strings.HasPrefix(last, "Describe") {
		return fake.Turn{Content: fmt.Sprintf("This request carries %d image(s). The demo model does not look at them.", images), Chunk: 16}
	}
	if strings.HasPrefix(last, "Summarize") {
		if rounds == 0 {
			return fake.Turn{ToolCalls: []fake.ToolCall{{ID: "c1", Name: "fs-list", Args: map[string]any{"path": "."}}, {ID: "c2", Name: "fs-read", Args: map[string]any{"path": "README.md"}}}}
		}
		return fake.Turn{Content: "## What's here\n\n- **README.md** — a one-line description\n- **app.js** — draws a session row\n- `notes/todo.md` — two open items\n\nNothing needed changing.", Chunk: 16}
	}
	if strings.HasPrefix(last, "Draft") {
		switch rounds {
		case 0:
			return fake.Turn{ToolCalls: []fake.ToolCall{{ID: "m1", Name: "chat__search_messages", Args: map[string]any{"query": "shipped this week"}}}}
		case 1:
			return fake.Turn{Content: "Here is the draft. I'll post it to #eng-status.", ToolCalls: []fake.ToolCall{{ID: "m2", Name: "chat__post_message", Args: map[string]any{"channel": "#eng-status", "text": "Week of 28 September\n\nShipped: cowork workspaces, attachments that read PDFs, phone approvals."}}}}
		default:
			return fake.Turn{Content: "## Week of 28 September\n\n**Shipped**\n\n- Cowork workspaces\n- Attachments that read PDFs\n- Phone approvals\n\nPosted to #eng-status.", Chunk: 20}
		}
	}
	if strings.HasPrefix(last, "Upload") {
		// With -docs: send the status to the fake Drive.
		if rounds == 0 {
			return fake.Turn{ToolCalls: []fake.ToolCall{{ID: "u1", Name: "google-doc-upload", Args: map[string]any{"path": "status.txt", "title": "Weekly status"}}}}
		}
		return fake.Turn{Content: "Done: the status is in Google Docs.", Chunk: 12}
	}
	if strings.HasPrefix(last, "Note") {
		// The contained server, once Connections has added and approved it.
		switch rounds {
		case 0:
			return fake.Turn{ToolCalls: []fake.ToolCall{{ID: "n1", Name: "notes__search_notes", Args: map[string]any{"query": "status"}}}}
		case 1:
			return fake.Turn{ToolCalls: []fake.ToolCall{{ID: "n2", Name: "notes__write_note", Args: map[string]any{"text": "Weekly status: phone approvals shipped."}}}}
		default:
			return fake.Turn{Content: "Noted.", Chunk: 12}
		}
	}
	if strings.HasPrefix(last, "Tidy") {
		// A turn that a rate limit ends after one step of its plan, so
		// "continue" carries the plan with that step marked.
		switch rounds {
		case 0:
			return fake.Turn{ToolCalls: []fake.ToolCall{{ID: "t1", Name: "plan-write", Args: map[string]any{"plan": "1. Change: notes/todo.md — put the open items in order.\n2. Test: none — the notes are not code.\n3. Docs: none — the notes are the doc.\n4. Check: cat notes/todo.md"}}}}
		case 1:
			return fake.Turn{ToolCalls: []fake.ToolCall{{ID: "t2", Name: "fs-read", Args: map[string]any{"path": "notes/todo.md"}}, planStep("t3", 1, "done", "two open items, already in order")}}
		default:
			return fake.Turn{Status: http.StatusTooManyRequests}
		}
	}
	if strings.HasPrefix(last, "Quiet") {
		// Two changes to one file in one fs-edit call (ADR 38): one
		// confirm, one diff, one write.
		if rounds == 0 {
			return fake.Turn{Content: "I'll silence the echo of both commands in one edit.", ToolCalls: []fake.ToolCall{{ID: "q1", Name: "fs-edit", Args: map[string]any{"path": "Makefile", "edits": []any{
				map[string]any{"old": "\tgo build ./...\n", "new": "\t@go build ./...\n"},
				map[string]any{"old": "\tgo test ./...\n", "new": "\t@go test ./... >/dev/null\n"},
			}}}}}
		}
		return fake.Turn{Content: "Quieted: make no longer echoes either command, and the tests print only failures.", Chunk: 16}
	}
	if strings.HasPrefix(last, "Write") {
		// A task in a worktree writes a file there, never in the
		// repository's own checkout (ADR 34).
		if rounds == 0 {
			return fake.Turn{ToolCalls: []fake.ToolCall{{ID: "f1", Name: "fs-write", Args: map[string]any{"path": "notes/" + strings.ToLower(strings.Fields(last)[len(strings.Fields(last))-1]) + ".md", "content": last + "\n"}}}}
		}
		return fake.Turn{Content: "Written.", Chunk: 12}
	}
	if last == "continue" {
		if rounds == 0 {
			return fake.Turn{ToolCalls: []fake.ToolCall{planStep("t4", 2, "done", "no code changed"), planStep("t5", 3, "done", "the notes are the doc"), planStep("t6", 4, "done", "two open items, in order")}}
		}
		return fake.Turn{Content: "Tidied: the notes were already in order.", Chunk: 12}
	}
	if !strings.HasPrefix(last, "The session list") {
		return fake.Turn{Content: "Glad that helps. Anything else in this folder?", Chunk: 12}
	}
	switch rounds {
	case 0:
		return fake.Turn{Thinking: "Start by finding where the list is drawn.", ToolCalls: []fake.ToolCall{
			{ID: "r1", Name: "fs-list", Args: map[string]any{"path": "."}},
			{ID: "r2", Name: "index-grep", Args: map[string]any{"path": ".", "pattern": "sessionRow"}},
		}}
	case 1:
		return fake.Turn{Thinking: "sessionRow draws the title alone; read it and the notes.", ToolCalls: []fake.ToolCall{
			{ID: "r3", Name: "fs-read", Args: map[string]any{"path": "app.js"}},
			{ID: "r4", Name: "fs-read", Args: map[string]any{"path": "notes/todo.md"}},
		}}
	case 2:
		return fake.Turn{Content: "A saved session already records its repository, when it last changed and whether a turn was still running. I'll put when it changed and whether it's running under each title, and the session id on hover.", ToolCalls: []fake.ToolCall{
			{ID: "w1", Name: "fs-edit", Args: map[string]any{"path": "app.js", "old": "  row.append(node(\"span\", session.repo, \"meta\"));\n", "new": "  const when = ago(session.lastEvent);\n  const state = session.inFlight ? \"running\" : \"idle\";\n  const meta = [session.repo, when, state].join(\" · \");\n  row.append(node(\"span\", meta, \"meta\"));\n  row.title = session.id;\n"}},
		}}
	case 3:
		// Two steps are marked before the commands that ask for approval
		// and two after them, so the browser check sees the count change.
		// The two commands in a row fold into one run in the transcript
		// (approve.js checks what it says while one waits and once both ran).
		return fake.Turn{ToolCalls: []fake.ToolCall{
			planStep("p1", 1, "done", "app.js shows when it changed and whether it is running"),
			planStep("p2", 2, "done", "failed before the change, passes after it"),
			{ID: "x1", Name: "shell-exec", Args: map[string]any{"cmd": "ls", "args": []any{"-la"}}},
			{ID: "x2", Name: "shell-exec", Args: map[string]any{"cmd": "wc", "args": []any{"-l", "app.js"}}},
			planStep("p3", 3, "skipped", "the notes are the person's list: left to them"),
			planStep("p4", 4, "done", "ls shows app.js changed; the demo has no phone to check on"),
		}}
	default:
		return fake.Turn{Content: "Done. Each row now shows **when it last changed** and **whether it's running**:\n\n```js\nconst meta = [session.repo, when, state].join(\" · \");\n```\n\nHover a row to see its id.", Chunk: 20}
	}
}
