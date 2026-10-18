package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/fclaude/nodwyn/internal/config"
)

// cli is one parsed command line: the shared flag set, the values it
// carries, and where nodwyn keeps its files. Every command is a method on it
// and returns an error; main prints and exits, so a command never has to.
type cli struct {
	fs    *flag.FlagSet
	paths config.Paths

	showVersion bool
	thinking    bool
	noConfirm   bool
	yolo        bool
	noKeychain  bool
	keychainTTY bool
	resume      string
	repo        string
	model       string
}

// commands maps the first argument to its handler. One rule governs flags:
// the shared flags above go before the command, and a command that parses
// flags of its own takes those after it. A command refuses every shared flag
// it does not read (only, or rejectGlobals for one that parses its own), so
// a flag in the wrong place is an error rather than a setting nothing sees.
var commands = map[string]func(*cli) error{
	"":           (*cli).chat,
	"chat":       (*cli).chat,
	"ui":         (*cli).ui,
	"app":        (*cli).app,
	"devices":    (*cli).devices,
	"versions":   (*cli).versions,
	"undo":       (*cli).undo,
	"tui":        (*cli).tui,
	"run":        (*cli).runTask,
	"daemon":     (*cli).daemon,
	"replay":     (*cli).replay,
	"summary":    (*cli).summary,
	"memory":     (*cli).memory,
	"sessions":   (*cli).sessions,
	"keychain":   (*cli).keychain,
	"credential": (*cli).credential,
	"mcp":        (*cli).mcp,
	"paths":      (*cli).showPaths,
	"trust":      (*cli).trust,
	"startup":    (*cli).startup,
	"repl":       (*cli).repl,
	"eval":       (*cli).eval,
}

func newCLI(args []string) *cli {
	c := &cli{fs: flag.NewFlagSet("nodwyn", flag.ExitOnError)}
	c.fs.BoolVar(&c.showVersion, "version", false, "print version and exit")
	c.fs.StringVar(&c.resume, "resume", "", "resume a task by its session id")
	c.fs.StringVar(&c.repo, "repo", ".", "repository root; every path the model touches is pinned under it")
	c.fs.StringVar(&c.model, "model", "", "the model a new task talks to: a [models.NAME], or a model on [control]'s server")
	c.fs.BoolVar(&c.thinking, "show-thinking", false, "show the model's thinking as it streams")
	c.fs.BoolVar(&c.noConfirm, "no-confirm", false, "answer every confirm and budget checkpoint with no, without reading a line (scripts)")
	c.fs.BoolVar(&c.yolo, "yolo", false, "turn on the task's Don't ask, which [approval] dont_ask must offer: ordinary writes and commands are approved without asking")
	c.fs.BoolVar(&c.noKeychain, "no-keychain", false, "leave the daemon's keychain locked instead of asking for its passphrase")
	c.fs.BoolVar(&c.keychainTTY, "keychain-tty", false, "ask for the daemon's keychain passphrase on the controlling terminal even when stdin is a script")
	// Line mode took these while it hosted its own session; a task in the
	// daemon has no form for them, and chat refuses each, saying where its
	// setting is now (ADR 30, amended 2026-10-17).
	c.fs.String("url", "", "no longer taken: name the server in config.toml")
	c.fs.String("budget-wall", "", "no longer taken: [budget] wall_hard in config.toml")
	c.fs.Int64("budget-tokens", 0, "no longer taken: [budget] tokens_hard in config.toml")
	c.fs.Int64("budget-actions", 0, "no longer taken: [budget] actions_hard in config.toml")
	c.fs.Float64("budget-cost", 0, "no longer taken: [budget] cost_hard in config.toml")
	c.fs.Int("max-tool-calls", 0, "no longer taken: [budget] tool_calls_hard in config.toml")
	c.fs.Bool("plain", true, "deprecated and ignored: line mode is the default, as nodwyn chat")
	c.fs.Usage = func() { fmt.Fprint(c.fs.Output(), usage); c.fs.PrintDefaults() }
	_ = c.fs.Parse(args)
	return c
}

// rest is everything after the command word; a command with its own flags
// parses these.
func (c *cli) rest() []string { return c.fs.Args()[1:] }

// set reports whether a shared flag was written on this command line, as
// opposed to holding its default.
func (c *cli) set(name string) bool {
	found := false
	c.fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// rejectGlobals refuses shared flags placed before a command that owns its
// own flags. format takes the offending flag's name; allowed names pass.
// The deprecated --plain does nothing anywhere, so it is never refused.
func (c *cli) rejectGlobals(format string, allowed ...string) error {
	var bad error
	c.fs.Visit(func(f *flag.Flag) {
		for _, ok := range append(allowed, "plain") {
			if f.Name == ok {
				return
			}
		}
		if bad == nil {
			bad = fmt.Errorf(format, f.Name)
		}
	})
	return bad
}

// only is the check of a command that reads some of the shared flags
// (allowed) and no flags of its own: the others are refused, and so is one
// written after the command, which the flag set stopped reading at the
// command word and would otherwise take for an argument.
func (c *cli) only(command string, allowed ...string) error {
	if err := c.rejectGlobals(command+": --%s is not an option of "+command, allowed...); err != nil {
		return err
	}
	return c.afterCommand(command)
}

// afterCommand refuses a shared flag written after its command.
func (c *cli) afterCommand(command string) error {
	for _, arg := range c.rest() {
		name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if strings.HasPrefix(arg, "-") && c.fs.Lookup(name) != nil {
			return fmt.Errorf("%s: put --%s before %s", command, name, command)
		}
	}
	return nil
}

// run dispatches. version and licenses answer before configuration is
// resolved, so they work even where nodwyn has no home directory.
func (c *cli) run() error {
	name := c.fs.Arg(0)
	switch {
	case c.showVersion || name == "version":
		fmt.Println("nodwyn", versionString())
		return nil
	case name == "licenses":
		fmt.Print(licenseText())
		return nil
	}
	handler, ok := commands[name]
	if !ok {
		return fmt.Errorf("unknown command %q", name)
	}
	paths, err := defaultPaths()
	if err != nil {
		return err
	}
	c.paths = paths
	return handler(c)
}

// Commands that delegate to a package with its own flag set.

func (c *cli) ui() error {
	if err := c.rejectGlobals("ui: put --%s after ui"); err != nil {
		return err
	}
	return runUI(c.paths, c.rest())
}

func (c *cli) tui() error {
	if err := c.rejectGlobals("tui: put --%s after tui"); err != nil {
		return err
	}
	return runTUI(c.paths, c.rest())
}

func (c *cli) runTask() error {
	if err := c.rejectGlobals("run: put --%s after run; the daemon owns configuration"); err != nil {
		return err
	}
	return runTask(c.rest())
}

func (c *cli) daemon() error {
	if err := c.rejectGlobals("daemon: --%s is not a daemon option", "no-keychain"); err != nil {
		return err
	}
	return runDaemon(c.paths, c.rest(), c.noKeychain)
}

func (c *cli) summary() error {
	if err := c.only("summary"); err != nil {
		return err
	}
	return summaryCommand(c.paths, c.rest())
}

func (c *cli) memory() error {
	if err := c.only("memory", "repo"); err != nil {
		return err
	}
	return memoryCommand(c.paths, c.repo, c.rest())
}

func (c *cli) showPaths() error {
	if err := c.only("paths"); err != nil {
		return err
	}
	fmt.Printf("config:   %s\nbindings: %s\nkeychain: %s\nsessions: %s\n",
		c.paths.ConfigFile, c.paths.Bindings, c.paths.Keychain, c.paths.SessionsDir)
	return nil
}
