// Command nibb is the entrypoint. It parses flags, resolves configuration,
// and hands a core session to a front-end (ADR 10). It contains no
// behaviour of its own.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/frontend/plain"
	"github.com/fclaude/nibb/internal/keychain"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

// version is set by the Makefile via -ldflags; "dev" builds fall back to VCS
// info from the build.
var version = "dev"

func main() {
	fs := flag.NewFlagSet("nibb", flag.ExitOnError)
	showVersion := fs.Bool("version", false, "print version and exit")
	resume := fs.String("resume", "", "resume a session by id")
	repo := fs.String("repo", ".", "repository root; every path the model touches is pinned under it")
	model := fs.String("model", "", "override control.model")
	url := fs.String("url", "", "override control.url")
	thinking := fs.Bool("show-thinking", false, "show the model's thinking as it streams")
	noConfirm := fs.Bool("no-confirm", false, "answer every confirm with no (non-interactive runs)")
	noKeychain := fs.Bool("no-keychain", false, "start without opening the keychain (no credential primitives)")
	budgetWall := fs.String("budget-wall", "", "hard turn wall limit, e.g. 2h")
	budgetTokens := fs.Int64("budget-tokens", 0, "hard reported model token limit per turn")
	budgetActions := fs.Int64("budget-actions", 0, "hard irreversible host-action limit per turn")
	maxCalls := fs.Int("max-tool-calls", 0, "tool calls allowed per turn before the hard stop (overrides budget.tool_calls_hard)")
	fs.Bool("plain", true, "line-mode front-end (the only one in bootstrap)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `usage: nibb [flags] [command]

commands:
  (none) | chat        start a session in the plain front-end
  replay SESSION       print a session log as a transcript (id or path)
  sessions             list sessions
  daemon [--socket PATH] serve local sessions, turns and events
  repl                 interactive Scheme REPL (reads stdin)
  eval FILE...         evaluate Scheme files
  eval -e EXPR         evaluate an expression and print its value
  keychain init        create the encrypted keychain (asks for a passphrase)
  paths                print the config and data locations
  version              print version

flags:
`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(os.Args[1:])

	cmd := fs.Arg(0)
	if *showVersion || cmd == "version" {
		fmt.Println("nibb", versionString())
		return
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		fail(err)
	}
	switch cmd {
	case "daemon":
		fs.Visit(func(f *flag.Flag) {
			if f.Name != "no-keychain" {
				fail(fmt.Errorf("daemon: --%s is not a daemon option", f.Name))
			}
		})
		if err := runDaemon(paths, fs.Args()[1:], *noKeychain); err != nil {
			fail(err)
		}
	case "", "chat":
		cfg, err := config.Load(paths.ConfigFile)
		if err != nil {
			fail(err)
		}
		if *model != "" {
			cfg.Control.Model = *model
		}
		if *url != "" {
			cfg.Control.URL = *url
		}
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "max-tool-calls":
				if *maxCalls <= 0 {
					fail(fmt.Errorf("max-tool-calls must be positive"))
				}
				cfg.Loop.MaxToolCalls = *maxCalls
			case "budget-tokens":
				if *budgetTokens <= 0 {
					fail(fmt.Errorf("budget-tokens must be positive"))
				}
				cfg.Budget.TokensHard = *budgetTokens
				cfg.Budget.TokensSoft = min(cfg.Budget.TokensSoft, *budgetTokens)
			case "budget-actions":
				if *budgetActions <= 0 {
					fail(fmt.Errorf("budget-actions must be positive"))
				}
				cfg.Budget.ActionsHard = *budgetActions
				cfg.Budget.ActionsSoft = min(cfg.Budget.ActionsSoft, *budgetActions)
			case "budget-wall":
				hard, err := time.ParseDuration(*budgetWall)
				if err != nil || hard <= 0 {
					fail(fmt.Errorf("budget-wall must be a positive duration"))
				}
				soft, _ := time.ParseDuration(cfg.Budget.WallSoft)
				cfg.Budget.WallHard = hard.String()
				cfg.Budget.WallSoft = min(soft, hard).String()
			}
		})
		if err := cfg.Validate(); err != nil {
			fail(err)
		}
		if *resume != "" {
			explicitRepo := false
			fs.Visit(func(f *flag.Flag) {
				if f.Name == "repo" {
					explicitRepo = true
				}
			})
			if !explicitRepo {
				*repo = ""
			}
		}
		runChat(cfg, paths, *repo, *thinking, *noConfirm, *noKeychain, *resume)
	case "replay":
		if fs.NArg() < 2 {
			fail(fmt.Errorf("replay: session id or path required"))
		}
		p := fs.Arg(1)
		if !strings.Contains(p, string(filepath.Separator)) && !strings.HasSuffix(p, ".jsonl") {
			p = log.SessionPath(paths.SessionsDir, p)
		}
		events, err := log.ReadFile(p)
		if err != nil && len(events) == 0 {
			fail(err)
		}
		plain.Replay(events, os.Stdout, *thinking)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nibb: note:", err)
		}
	case "sessions":
		infos, err := (core.Store{Dir: paths.SessionsDir}).List()
		if err != nil {
			fail(err)
		}
		ids := fs.Arg(1) == "--ids"
		if fs.NArg() > 2 || (fs.NArg() == 2 && !ids) {
			fail(fmt.Errorf("sessions: expected --ids or no arguments"))
		}
		for _, info := range infos {
			if ids {
				fmt.Println(info.ID)
				continue
			}
			state := "idle"
			if info.InFlight {
				state = "interrupted"
			}
			if info.Truncated {
				state += " (partial tail)"
			}
			fmt.Printf("%s\t%s\t%s\t%q\t%q\n", info.ID, info.LastEvent.Format("2006-01-02 15:04:05Z07:00"), state, info.Repo, info.FirstMessage)
		}
	case "keychain":
		if fs.Arg(1) != "init" {
			fail(fmt.Errorf("keychain: expected 'init'"))
		}
		if _, err := os.Stat(paths.Keychain); err == nil {
			fail(fmt.Errorf("keychain already exists at %s", paths.Keychain))
		}
		pass := readPassphrase("new keychain passphrase: ")
		if string(pass) != string(readPassphrase("again: ")) {
			fail(fmt.Errorf("passphrases do not match"))
		}
		if len(pass) == 0 {
			fail(fmt.Errorf("empty passphrase"))
		}
		if err := os.MkdirAll(filepath.Dir(paths.Keychain), 0o700); err != nil {
			fail(err)
		}
		if _, err := keychain.Create(paths.Keychain, keychain.DefaultKDF, pass); err != nil {
			fail(err)
		}
		fmt.Println("keychain created at", paths.Keychain)
	case "paths":
		fmt.Printf("config:   %s\nbindings: %s\nkeychain: %s\nsessions: %s\n", paths.ConfigFile, paths.Bindings, paths.Keychain, paths.SessionsDir)
	case "repl":
		in := scheme.New(scheme.WithOutput(os.Stdout))
		if err := plain.SchemeREPL(context.Background(), in, os.Stdin, os.Stdout, true); err != nil {
			fail(err)
		}
	case "eval":
		runEval(fs.Args()[1:])
	default:
		fail(fmt.Errorf("unknown command %q", cmd))
	}
}

// readPassphrase reads a masked line from the terminal. Passphrases never
// come from the environment or from arguments (plan §3.3).
func readPassphrase(prompt string) []byte {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fail(fmt.Errorf("a passphrase needs a terminal"))
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		fail(err)
	}
	return b
}

// openKeychain opens the keychain if one exists and stdin is a terminal.
// Without one the session has no credential primitives (bootstrap mode).
func openKeychain(paths config.Paths) (*keychain.Store, binding.Table) {
	if _, err := os.Stat(paths.Keychain); err != nil {
		return nil, binding.Table{}
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "keychain: not a terminal; starting without credentials (use --no-keychain to silence)")
		return nil, binding.Table{}
	}
	for attempt := 0; attempt < 3; attempt++ {
		kc, err := keychain.Open(paths.Keychain, readPassphrase("keychain passphrase: "))
		if err == nil {
			tbl, err := binding.Load(paths.Bindings)
			if err != nil {
				fail(err)
			}
			fmt.Fprintf(os.Stderr, "keychain: %d credential(s), %d binding row(s)\n", kc.Count(), len(tbl.Bind))
			return kc, tbl
		}
		fmt.Fprintln(os.Stderr, "keychain:", err)
	}
	fail(fmt.Errorf("keychain: could not open"))
	return nil, binding.Table{}
}

func runChat(cfg config.Config, paths config.Paths, repo string, thinking, noConfirm, noKeychain bool, resume string) {
	provider := llm.NewOllama(cfg.Control.URL)
	ctx := context.Background()
	if v, err := provider.Ping(ctx); err != nil {
		fail(fmt.Errorf("control plane %s unreachable: %w", cfg.Control.URL, err))
	} else {
		fmt.Fprintf(os.Stderr, "control plane: %s %s (ollama %s)\n", cfg.Control.URL, cfg.Control.Model, v)
	}
	var kc *keychain.Store
	var tbl binding.Table
	if !noKeychain {
		kc, tbl = openKeychain(paths)
	}
	options := core.Options{
		Config: cfg, Paths: paths, Repo: repo, Version: versionString(), Provider: provider, NoConfirm: noConfirm,
		Keychain: kc, Bindings: tbl,
	}
	var s *core.Session
	var err error
	if resume == "" {
		s, err = core.New(options)
	} else {
		s, err = core.Resume(options, resume)
	}
	if err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "log: %s\n", log.SessionPath(paths.SessionsDir, s.ID()))

	interrupt := make(chan struct{}, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		for range sig {
			if s.Busy() {
				select {
				case interrupt <- struct{}{}:
				default:
				}
				continue
			}
			_ = s.Close()
			fmt.Fprintln(os.Stderr)
			os.Exit(130)
		}
	}()

	chat := &plain.Chat{Session: s, In: os.Stdin, Out: os.Stdout, ShowThinking: thinking, Interrupt: interrupt, ReplayHistory: resume != ""}
	runErr := chat.Run(ctx)
	if err := s.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "nibb: close:", err)
	}
	if runErr != nil {
		fail(runErr)
	}
}

func runEval(args []string) {
	in := scheme.New(scheme.WithOutput(os.Stdout))
	ctx := context.Background()
	if len(args) >= 2 && args[0] == "-e" {
		v, err := in.EvalString(ctx, args[1])
		if err != nil {
			fail(err)
		}
		if v.Kind != scheme.KVoid {
			fmt.Println(scheme.Write(v))
		}
		return
	}
	if len(args) == 0 {
		fail(fmt.Errorf("eval: expected files or -e EXPR"))
	}
	for _, path := range args {
		if err := in.LoadFile(ctx, path); err != nil {
			fail(err)
		}
	}
}

func versionString() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		rev, dirty := "", ""
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if len(s.Value) >= 12 {
					rev = s.Value[:12]
				}
			case "vcs.modified":
				if s.Value == "true" {
					dirty = "-dirty"
				}
			}
		}
		if rev != "" {
			return "dev-" + rev + dirty
		}
	}
	return version
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "nibb:", err)
	os.Exit(1)
}
