package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/plain"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"golang.org/x/term"
)

// unhosted are the flags line mode took while it ran its session in its own
// process, which a task in the daemon has no form for (ADR 30, amended
// 2026-10-17): each is refused, saying where its setting is now. Its limit
// flags are the task's own limits again (ADR 49, amended 2026-10-27).
var unhosted = []struct{ flag, instead string }{
	{"url", "the daemon reaches only the servers config.toml names: set this one as [control]'s url, or add a [models.NAME] on it and pass --model NAME"},
}

// chat is the default command: one task in the line-mode front-end, over
// the daemon, as every client is (ADR 30, amended 2026-10-17).
func (c *cli) chat() error {
	// chat reads the shared flags, which go before it, and nothing else.
	if c.fs.NArg() > 1 {
		if err := c.afterCommand("chat"); err != nil {
			return err
		}
		return fmt.Errorf("chat: unexpected argument %q", c.rest()[0])
	}
	for _, u := range unhosted {
		if c.set(u.flag) {
			return fmt.Errorf("chat: --%s is no longer taken: %s", u.flag, u.instead)
		}
	}
	// A limit the daemon would refuse is said before a daemon starts.
	if _, err := c.limits.over(budget.Task{}); err != nil {
		return fmt.Errorf("chat: %w", err)
	}
	cfg, err := config.Load(c.paths.ConfigFile)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	choice, err := c.modelChoice(cfg)
	if err != nil {
		return err
	}
	return c.session(cfg, choice)
}

// modelChoice is the model --model asks for: one config.toml names
// ([models.NAME], or "control"), or else that model on [control]'s server.
// A model at an address no configuration names has no form in the daemon,
// which talks only to the servers a person set up.
func (c *cli) modelChoice(cfg config.Config) (core.ModelChoice, error) {
	if c.model == "" {
		return core.ModelChoice{}, nil
	}
	if _, named := cfg.Models[c.model]; named || c.model == config.ControlModel {
		return core.ModelChoice{Name: c.model}, nil
	}
	if cfg.Control.Server == "" {
		return core.ModelChoice{}, fmt.Errorf("chat: --model %s: no [models.%s] is configured, and [control] names no [servers.NAME] for it to be on; add either to config.toml", c.model, c.model)
	}
	return core.ModelChoice{Server: cfg.Control.Server, Model: c.model}, nil
}

// session opens or resumes a task in the daemon, starting one in this
// process when none answers, and runs the line-mode front-end over it until
// the reader quits.
func (c *cli) session(cfg config.Config, choice core.ModelChoice) error {
	m, _, err := core.Choose(cfg, choice)
	if err != nil {
		return fmt.Errorf("chat: %w", err)
	}
	if m.URL == "" || m.Model == "" {
		return config.MissingControl(c.paths.ConfigFile)
	}
	label := "control model"
	if choice.Name != "" && choice.Name != config.ControlModel {
		label = "model " + choice.Name
	}
	if m.Provider == "" || m.Provider == "ollama" {
		version, err := llm.NewOllama(m.URL).Ping(context.Background())
		if err != nil {
			return fmt.Errorf("%s %s unreachable: %w", label, m.URL, err)
		}
		fmt.Fprintf(os.Stderr, "%s: %s %s (ollama %s)\n", label, m.URL, m.Model, version)
	} else {
		// Only Ollama answers /api/version. Any other provider's first
		// request reports a server that cannot be reached.
		fmt.Fprintf(os.Stderr, "%s: %s %s (%s)\n", label, m.URL, m.Model, m.Provider)
	}

	socket, err := rpc.DefaultSocket()
	if err != nil {
		return err
	}
	// A daemon this process starts lives as long as line mode does.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owned, daemonDone, err := ensureDaemon(ctx, c.paths, socket, "chat")
	if err != nil {
		return err
	}
	if owned {
		fmt.Fprintln(os.Stderr, "chat: serving tasks in this process on", socket+"; quitting ends them")
	} else {
		fmt.Fprintln(os.Stderr, "chat: using the daemon already running on", socket)
	}
	var once sync.Once
	var stopErr error
	stop := func() error {
		once.Do(func() {
			cancel()
			if daemonDone != nil {
				stopErr = <-daemonDone
			}
		})
		return stopErr
	}
	err = c.attach(ctx, socket, choice, stop)
	return errors.Join(err, stop())
}

// attach unlocks the daemon's keychain if it is locked, opens the task and
// shows it, and leaves it as quitting does. stop ends a daemon this process
// serves, before a Ctrl-C exits.
func (c *cli) attach(ctx context.Context, socket string, choice core.ModelChoice, stop func() error) error {
	if !c.noKeychain {
		read, done := c.passphrases()
		err := unlockDaemon(ctx, socket, read, os.Stderr)
		done()
		if err != nil {
			return err
		}
	}
	client, err := rpc.Dial(ctx, socket)
	if err != nil {
		return err
	}
	defer client.Close()
	info, opened, err := c.open(ctx, client, choice)
	if err != nil {
		return err
	}
	var once sync.Once
	var leaveErr error
	leave := func() error {
		once.Do(func() { leaveErr = c.leave(ctx, client, info.ID, opened) })
		return leaveErr
	}
	// A daemon started with another data directory (another XDG_DATA_HOME,
	// a test's or a script's) keeps the task elsewhere, and its own
	// configuration chose what the task may reach: it is not this command's.
	if _, err := os.Stat(log.SessionPath(c.paths.SessionsDir, info.ID)); err != nil {
		return errors.Join(fmt.Errorf("chat: the daemon on %s keeps its tasks outside %s, so it was started with another configuration; stop it, or give this command a runtime directory of its own (XDG_RUNTIME_DIR)", socket, c.paths.SessionsDir), leave())
	}
	if err := c.prepare(ctx, client, info, choice); err != nil {
		return errors.Join(err, leave())
	}
	fmt.Fprintf(os.Stderr, "log: %s\n", log.SessionPath(c.paths.SessionsDir, info.ID))

	chat := &plain.Chat{
		Client: client, ID: info.ID, In: os.Stdin, Out: os.Stdout,
		ShowThinking: c.thinking, NoConfirm: c.noConfirm, ReplayHistory: c.resume != "",
	}
	chat.Interrupt = watchInterrupts(chat, func() {
		if err := errors.Join(leave(), stop()); err != nil {
			fmt.Fprintln(os.Stderr, "nodwyn:", err)
		}
	})
	runErr := chat.Run(ctx)
	return errors.Join(runErr, leave())
}

// open opens the task in the daemon: a new one in --repo, or the stored one
// --resume names. opened is false for a task the daemon already had open
// for another client, which line mode leaves open when it quits.
func (c *cli) open(ctx context.Context, client *rpc.Client, choice core.ModelChoice) (info rpc.SessionDetails, opened bool, err error) {
	if c.resume == "" {
		root, err := filepath.Abs(c.repo)
		if err != nil {
			return info, false, err
		}
		params := map[string]any{"repo": root}
		if len(c.limits.given()) > 0 {
			limits, err := c.limits.over(budget.Task{})
			if err != nil {
				return info, false, err
			}
			params["limits"] = limits
		}
		if choice.Name != "" {
			params["name"] = choice.Name
		} else if choice.Server != "" {
			params["server"], params["model"] = choice.Server, choice.Model
		}
		err = client.Call(ctx, "session.open", params, &info)
		return info, err == nil, err
	}
	if c.set("repo") {
		// A resumed task keeps the folder it was opened in; naming another
		// is a mistake to say, before the task is resumed.
		if err := c.checkFolder(ctx, client); err != nil {
			return info, false, err
		}
	}
	if client.Call(ctx, "session.info", map[string]any{"id": c.resume}, &info) == nil {
		return info, false, nil
	}
	err = client.Call(ctx, "session.resume", map[string]any{"id": c.resume}, &info)
	return info, err == nil, err
}

// checkFolder refuses --repo naming a folder the resumed task is not in.
func (c *cli) checkFolder(ctx context.Context, client *rpc.Client) error {
	root, err := filepath.Abs(c.repo)
	if err != nil {
		return err
	}
	var tasks []rpc.SessionInfo
	if err := client.Call(ctx, "session.list", struct{}{}, &tasks); err != nil {
		return err
	}
	for _, t := range tasks {
		if t.ID == c.resume && config.CanonicalPath(t.Repo) != config.CanonicalPath(root) {
			return fmt.Errorf("chat: task %s belongs to %s, not %s", c.resume, t.Repo, root)
		}
	}
	return nil
}

// prepare makes the opened task what this command line asks: on the model
// --model names when it was resumed, with the limits it names in place of
// the task's own, and with Don't ask on for --yolo.
func (c *cli) prepare(ctx context.Context, client *rpc.Client, info rpc.SessionDetails, choice core.ModelChoice) error {
	if c.resume != "" && len(c.limits.given()) > 0 {
		limits, err := c.limits.over(info.Limits)
		if err != nil {
			return err
		}
		if err := client.Call(ctx, "session.limits.set", map[string]any{"id": info.ID, "limits": limits}, nil); err != nil {
			return fmt.Errorf("chat: limits: %w", err)
		}
		fmt.Fprintf(os.Stderr, "limits: %s\n", limits)
	} else if len(c.limits.given()) > 0 {
		fmt.Fprintf(os.Stderr, "limits: %s\n", info.Limits)
	}
	if c.resume != "" && choice != (core.ModelChoice{}) {
		params := map[string]any{"id": info.ID, "name": choice.Name}
		if choice.Server != "" {
			params = map[string]any{"id": info.ID, "server": choice.Server, "model": choice.Model}
		}
		if err := client.Call(ctx, "session.model.set", params, nil); err != nil {
			return fmt.Errorf("chat: --model: %w", err)
		}
	}
	if !c.yolo {
		return nil
	}
	if err := client.Call(ctx, "session.approve_all", map[string]any{"id": info.ID, "on": true}, nil); err != nil {
		return fmt.Errorf("chat: --yolo turns on Don't ask for the task: %w", err)
	}
	// Unattended approval is worth saying out loud, every time.
	security := "still ask"
	if c.noConfirm {
		security = "are declined"
	}
	fmt.Fprintf(os.Stderr, "yolo: Don't ask is on in %s: ordinary writes and commands are approved without asking; security confirms %s, and denials still stop the model\n", info.Repo, security)
	return nil
}

// leave leaves the task as line mode found it: one it opened is closed, as
// quitting always did, its log released; one another client had open stays
// open there, without the Don't ask --yolo turned on.
func (c *cli) leave(ctx context.Context, client *rpc.Client, id string, opened bool) error {
	if opened {
		return client.Call(ctx, "session.close", map[string]any{"id": id}, nil)
	}
	if c.yolo {
		if err := client.Call(ctx, "session.approve_all", map[string]any{"id": id, "on": false}, nil); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stderr, "chat: the task stays open in the daemon, as it was")
	return nil
}

// passphrases reads the keychain's passphrase from the terminal line mode
// runs in, or with --keychain-tty from the controlling terminal when its
// input is a script, as ssh and sudo ask. With neither, nobody is asked and
// the daemon's keychain stays locked. done closes what it opened.
func (c *cli) passphrases() (read func(string) ([]byte, error), done func()) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return func(prompt string) ([]byte, error) { return readPassphraseFrom(os.Stdin, os.Stderr, prompt) }, func() {}
	}
	if c.keychainTTY {
		if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
			if term.IsTerminal(int(tty.Fd())) {
				// The prompt is for the person at the terminal; stderr may
				// be a log.
				return func(prompt string) ([]byte, error) { return readPassphraseFrom(tty, tty, prompt) }, func() { tty.Close() }
			}
			tty.Close()
		}
	}
	return func(string) ([]byte, error) {
		fmt.Fprintln(os.Stderr, "keychain: not a terminal to ask for its passphrase on (--keychain-tty asks the controlling terminal)")
		return nil, nil
	}, func() {}
}

// watchInterrupts turns Ctrl-C into a turn abort while a turn line mode
// started runs, and otherwise into quit and an exit.
func watchInterrupts(chat *plain.Chat, quit func()) <-chan struct{} {
	interrupt := make(chan struct{}, 1)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		for range signals {
			if chat.Following() {
				select {
				case interrupt <- struct{}{}:
				default:
				}
				continue
			}
			quit()
			fmt.Fprintln(os.Stderr)
			os.Exit(130)
		}
	}()
	return interrupt
}
