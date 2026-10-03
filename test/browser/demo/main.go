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
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
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
	fakecontainer "github.com/fclaude/nodwyn/test/fakes/container"
	fakedrive "github.com/fclaude/nodwyn/test/fakes/drive"
	fakemcp "github.com/fclaude/nodwyn/test/fakes/mcp"
	fakeoauth "github.com/fclaude/nodwyn/test/fakes/oauth"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// notesImage is the image of the demo's contained server.
const notesImage = "ghcr.io/example/mcp-notes@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func main() {
	// Run again by the fake container runtime, this program is the runtime.
	fakecontainer.Main(notesTools()...)
	seed := flag.Bool("seed", false, "start demo tasks so every view has content")
	port := flag.Int("port", 0, "loopback port for the console (0 picks one)")
	modelOnly := flag.Bool("model-only", false, "run only the scripted model server and print its URL")
	docs := flag.Bool("docs", false, "keep a Google credential in a throwaway keychain, bound to a fake Drive that takes uploads")
	oauthOnly := flag.Bool("oauth-only", false, "run only a fake OAuth2 provider (client id demo-client) and print its endpoints")
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
	if err := run(*seed, *port, *docs); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

func run(seed bool, port int, docs bool) error {
	root, err := os.MkdirTemp("/tmp", "nodwyn-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	model := fake.NewFunc(respond)
	defer model.Close()
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
	listener, err := rpc.Listen(socket)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	daemonDone := make(chan error, 1)
	go func() { daemonDone <- rpc.Serve(ctx, listener, options) }()

	repos := map[string]string{}
	for _, name := range []string{"console", "receipts"} {
		dir := filepath.Join(root, "work", name)
		repos[name] = dir
		if err := writeRepo(dir, name); err != nil {
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
	fmt.Printf("URL %s/#ticket=%s\nCODE %s\nREPO %s\nMCP %s\nIMAGE %s\n", url, ticket, code, repos["console"], chat.URL, notesImage)
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
		// and the model's step marks to show.
		files["status.txt"] = "Week of 28 September: cowork workspaces, attachments that read PDFs, phone approvals.\n"
		files[".nodwyn/init.scm"] = "(define plan-explore-rounds 0)\n(define (plan-gate request) (if (plan-recorded?) #f (plan-best #f request \"plan\")))\n"
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

// seedTasks starts one task that stops at an approval and one that finishes.
func seedTasks(ctx context.Context, socket string, repos map[string]string) error {
	dial, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, err := rpc.Dial(dial, socket)
	if err != nil {
		return err
	}
	defer client.Close()
	start := func(repo, text string) error {
		var info struct {
			ID string `json:"id"`
		}
		if err := client.Call(ctx, "session.open", map[string]any{"repo": repo}, &info); err != nil {
			return err
		}
		return client.Call(ctx, "session.send", map[string]any{"id": info.ID, "text": text}, nil)
	}
	if err := start(repos["receipts"], "Summarize what is in this folder"); err != nil {
		return err
	}
	time.Sleep(1500 * time.Millisecond)
	return start(repos["console"], "The session list shows each session's first message, cut short, so two sessions that start the same way look identical. Make them easy to tell apart. Keep the change inside the web console.")
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
		case m.Role == "user" && !strings.HasPrefix(m.Content, "["):
			last, rounds = m.Content, 0
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			rounds++
		}
	}
	time.Sleep(400 * time.Millisecond)
	if len(req.Tools) == 0 {
		return fake.Turn{Content: "1. Find where the list is drawn\n2. Check what a saved session records\n3. Show when it changed and whether it's running\n4. Check it at phone width", Chunk: 24}
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
			planStep("p1", 1, "done", "sessionRow in app.js draws each row"),
		}}
	case 2:
		return fake.Turn{Content: "A saved session already records its repository, when it last changed and whether a turn was still running. I'll put when it changed and whether it's running under each title, and the session id on hover.", ToolCalls: []fake.ToolCall{
			planStep("p2", 2, "done", "a saved session records its repository, last change and whether a turn runs"),
			{ID: "w1", Name: "fs-edit", Args: map[string]any{"path": "app.js", "old": "  row.append(node(\"span\", session.repo, \"meta\"));\n", "new": "  const when = ago(session.lastEvent);\n  const state = session.inFlight ? \"running\" : \"idle\";\n  const meta = [session.repo, when, state].join(\" · \");\n  row.append(node(\"span\", meta, \"meta\"));\n  row.title = session.id;\n"}},
		}}
	case 3:
		return fake.Turn{ToolCalls: []fake.ToolCall{
			planStep("p3", 3, "done", "app.js shows when it changed and whether it is running"),
			{ID: "x1", Name: "shell-exec", Args: map[string]any{"cmd": "ls", "args": []any{"-la"}}},
			planStep("p4", 4, "skipped", "the demo has no phone to check on"),
		}}
	default:
		return fake.Turn{Content: "Done. Each row now shows **when it last changed** and **whether it's running**:\n\n```js\nconst meta = [session.repo, when, state].join(\" · \");\n```\n\nHover a row to see its id.", Chunk: 20}
	}
}
