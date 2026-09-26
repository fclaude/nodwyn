package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/frontend/tui"
	"golang.org/x/term"
)

func runTUI(paths config.Paths, args []string) error {
	flags := flag.NewFlagSet("nibb tui", flag.ContinueOnError)
	socket := flags.String("socket", "", "attach to a running daemon's Unix socket")
	repo := flags.String("repo", ".", "repository for new sessions")
	resume := flags.String("resume", "", "open this session directly")
	noKeychain := flags.Bool("no-keychain", false, "run locally without opening the keychain")
	thinking := flags.Bool("show-thinking", false, "show model thinking")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("tui: unexpected arguments")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("tui requires a terminal; use --plain for pipes")
	}
	if *socket != "" && *noKeychain {
		return fmt.Errorf("tui: --no-keychain applies only to local sessions; the daemon owns its keychain")
	}
	root, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	o := core.Options{Paths: paths, Repo: root, Version: versionString()}
	if *socket == "" {
		o.Config, err = config.Load(paths.ConfigFile)
		if err != nil {
			return err
		}
		if !*noKeychain {
			if o.Keychain, o.Bindings, err = openKeychain(paths); err != nil {
				return err
			}
		}
	}
	return tui.Run(context.Background(), tui.Options{Core: o, Socket: *socket, Resume: *resume, ShowThinking: *thinking, Input: os.Stdin, Output: os.Stdout})
}
