package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/frontend/tui"
	"golang.org/x/term"
)

// runTUI shows the terminal client over the daemon, as every client is
// (ADR 30, amended 2026-10-17): the one answering on the
// socket, or one this process starts when none answers, as nodwyn app
// starts one.
func runTUI(paths config.Paths, args []string) error {
	flags := flag.NewFlagSet("nodwyn tui", flag.ContinueOnError)
	socket := flags.String("socket", "", "daemon socket path (default: the standard one); a daemon is started in this process when none answers")
	repo := flags.String("repo", ".", "repository for new tasks")
	resume := flags.String("resume", "", "open this task directly, by its session id")
	worktree := flags.Bool("worktree", false, "start a new task directly, in a new git worktree of --repo")
	noKeychain := flags.Bool("no-keychain", false, "leave the daemon's keychain locked instead of asking for its passphrase")
	thinking := flags.Bool("show-thinking", false, "show model thinking")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("tui: unexpected arguments")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("tui requires a terminal; use nodwyn chat (line mode) for pipes")
	}
	if *worktree && *resume != "" {
		return fmt.Errorf("tui: --worktree starts a new task; a resumed one keeps the folder it has")
	}
	root, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	if *socket == "" {
		if *socket, err = rpc.DefaultSocket(); err != nil {
			return err
		}
	}
	// A daemon this process starts lives as long as the TUI does.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owned, daemonDone, err := ensureDaemon(ctx, paths, *socket, "tui")
	if err != nil {
		return err
	}
	if owned {
		fmt.Fprintln(os.Stderr, "tui: serving tasks in this process on", *socket+"; quitting ends them")
	}
	if !*noKeychain {
		err = unlockDaemon(ctx, *socket, func(prompt string) ([]byte, error) { return readPassphraseFrom(os.Stdin, os.Stderr, prompt) }, os.Stderr)
	}
	if err == nil {
		// Only now: Ctrl+C at the passphrase prompt must still end the
		// process, and in the TUI it is a key, not a signal.
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		err = tui.Run(runCtx, tui.Options{Socket: *socket, Owned: owned, Repo: root, Resume: *resume, Worktree: *worktree, ShowThinking: *thinking, Input: os.Stdin, Output: os.Stdout})
		stop()
	}
	cancel()
	if daemonDone != nil {
		err = errors.Join(err, <-daemonDone)
	}
	return err
}

// unlockDaemon asks for the keychain's passphrase when the daemon has it
// locked, as the console's unlock form does, so the tasks it serves can use
// their credentials: a daemon nodwyn app started, or one whose keychain was
// sealed again elsewhere. keychain.unlock is a method for this computer,
// and the socket is this computer's. An empty answer leaves it locked.
func unlockDaemon(ctx context.Context, socket string, read func(prompt string) ([]byte, error), out io.Writer) error {
	c, err := rpc.Dial(ctx, socket)
	if err != nil {
		return err
	}
	defer c.Close()
	var status struct {
		Exists   bool `json:"exists"`
		Unlocked bool `json:"unlocked"`
	}
	if err := c.Call(ctx, "keychain.status", struct{}{}, &status); err != nil {
		return err
	}
	if !status.Exists || status.Unlocked {
		return nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		pass, err := read("keychain passphrase (Enter leaves it locked): ")
		if err != nil {
			return err
		}
		if len(pass) == 0 {
			fmt.Fprintln(out, "keychain: left locked; tasks have no credentials until it is unlocked here or in the console")
			return nil
		}
		err = c.Call(ctx, "keychain.unlock", map[string]string{"passphrase": string(pass)}, nil)
		clear(pass)
		if err == nil {
			return nil
		}
		fmt.Fprintln(out, "keychain:", err)
	}
	return fmt.Errorf("keychain: could not unlock")
}
