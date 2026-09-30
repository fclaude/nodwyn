// Command demo runs the browser console against synthetic work: a fake model
// that looks around a scratch folder, proposes an edit, runs a command and
// answers, a daemon on temporary directories, and the console bridge. It is
// for looking at the console and for browser acceptance checks, never for
// real work: every directory is temporary and the model is scripted.
//
//	go run ./test/browser/demo            # prints the URL (with a launch ticket)
//	go run ./test/browser/demo -seed      # also starts tasks so every view has content
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

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/frontend/rpc"
	webui "github.com/fclaude/nibb/internal/frontend/web"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

func main() {
	seed := flag.Bool("seed", false, "start demo tasks so every view has content")
	port := flag.Int("port", 0, "loopback port for the console (0 picks one)")
	modelOnly := flag.Bool("model-only", false, "run only the scripted model server and print its URL")
	flag.Parse()
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
	if err := run(*seed, *port); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

func run(seed bool, port int) error {
	root, err := os.MkdirTemp("/tmp", "nibb-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	model := fake.NewFunc(respond)
	defer model.Close()

	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	cfg := config.Default()
	cfg.Control = config.Model{URL: model.URL, Model: "demo"}
	cfg.Jev.Content = config.JevContentNone
	options := core.Options{Config: cfg, Paths: paths, Version: "demo"}

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
	fmt.Printf("URL %s/#ticket=%s\nCODE %s\nREPO %s\n", url, ticket, code, repos["console"])
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

// respond is the scripted model. A request offering no tools is the planner;
// otherwise the reply depends on how many tool rounds this ask has had.
func respond(req fake.Request) fake.Turn {
	var last string
	rounds := 0
	for _, raw := range req.Messages {
		var m struct {
			Role      string            `json:"role"`
			Content   string            `json:"content"`
			ToolCalls []json.RawMessage `json:"tool_calls"`
		}
		_ = json.Unmarshal(raw, &m)
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
	if strings.HasPrefix(last, "Summarize") {
		if rounds == 0 {
			return fake.Turn{ToolCalls: []fake.ToolCall{{ID: "c1", Name: "fs-list", Args: map[string]any{"path": "."}}, {ID: "c2", Name: "fs-read", Args: map[string]any{"path": "README.md"}}}}
		}
		return fake.Turn{Content: "## What's here\n\n- **README.md** — a one-line description\n- **app.js** — draws a session row\n- `notes/todo.md` — two open items\n\nNothing needed changing.", Chunk: 16}
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
		return fake.Turn{ToolCalls: []fake.ToolCall{
			{ID: "r3", Name: "fs-read", Args: map[string]any{"path": "app.js"}},
			{ID: "r4", Name: "fs-read", Args: map[string]any{"path": "notes/todo.md"}},
		}}
	case 2:
		return fake.Turn{Content: "A saved session already records its repository, when it last changed and whether a turn was still running. I'll put when it changed and whether it's running under each title, and the session id on hover.", ToolCalls: []fake.ToolCall{
			{ID: "w1", Name: "fs-edit", Args: map[string]any{"path": "app.js", "old": "  row.append(node(\"span\", session.repo, \"meta\"));\n", "new": "  const when = ago(session.lastEvent);\n  const state = session.inFlight ? \"running\" : \"idle\";\n  const meta = [session.repo, when, state].join(\" · \");\n  row.append(node(\"span\", meta, \"meta\"));\n  row.title = session.id;\n"}},
		}}
	case 3:
		return fake.Turn{ToolCalls: []fake.ToolCall{{ID: "x1", Name: "shell-exec", Args: map[string]any{"cmd": "ls", "args": []any{"-la"}}}}}
	default:
		return fake.Turn{Content: "Done. Each row now shows **when it last changed** and **whether it's running**:\n\n```js\nconst meta = [session.repo, when, state].join(\" · \");\n```\n\nHover a row to see its id.", Chunk: 20}
	}
}
