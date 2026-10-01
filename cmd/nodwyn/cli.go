package main

import (
	"flag"
	"fmt"

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
	url         string
	budgetWall  string

	budgetTokens  int64
	budgetActions int64
	maxCalls      int
}

// commands maps the first argument to its handler. Two kinds live here:
// those that read the shared flags above, and those that parse their own
// from the remaining arguments — the latter start with rejectGlobals, so a
// shared flag written before the command is an error rather than a setting
// the command never sees.
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
	"sessions":   (*cli).sessions,
	"keychain":   (*cli).keychain,
	"credential": (*cli).credential,
	"paths":      (*cli).showPaths,
	"repl":       (*cli).repl,
	"eval":       (*cli).eval,
}

func newCLI(args []string) *cli {
	c := &cli{fs: flag.NewFlagSet("nodwyn", flag.ExitOnError)}
	c.fs.BoolVar(&c.showVersion, "version", false, "print version and exit")
	c.fs.StringVar(&c.resume, "resume", "", "resume a session by id")
	c.fs.StringVar(&c.repo, "repo", ".", "repository root; every path the model touches is pinned under it")
	c.fs.StringVar(&c.model, "model", "", "override control.model")
	c.fs.StringVar(&c.url, "url", "", "override control.url")
	c.fs.BoolVar(&c.thinking, "show-thinking", false, "show the model's thinking as it streams")
	c.fs.BoolVar(&c.noConfirm, "no-confirm", false, "answer every confirm with no (non-interactive runs)")
	c.fs.BoolVar(&c.yolo, "yolo", false, "answer ordinary confirms with yes (unattended runs); denials and security confirms are unaffected")
	c.fs.BoolVar(&c.noKeychain, "no-keychain", false, "start without opening the keychain (no credential primitives)")
	c.fs.BoolVar(&c.keychainTTY, "keychain-tty", false, "ask for the keychain passphrase on the controlling terminal even when stdin is a script")
	c.fs.StringVar(&c.budgetWall, "budget-wall", "", "hard turn wall limit, e.g. 2h")
	c.fs.Int64Var(&c.budgetTokens, "budget-tokens", 0, "hard reported model token limit per turn")
	c.fs.Int64Var(&c.budgetActions, "budget-actions", 0, "hard irreversible host-action limit per turn")
	c.fs.IntVar(&c.maxCalls, "max-tool-calls", 0, "tool calls allowed per turn before the hard stop (overrides budget.tool_calls_hard)")
	c.fs.Bool("plain", true, "use the line-mode front-end")
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
func (c *cli) rejectGlobals(format string, allowed ...string) error {
	var bad error
	c.fs.Visit(func(f *flag.Flag) {
		for _, ok := range allowed {
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

func (c *cli) summary() error { return summaryCommand(c.paths, c.rest()) }

func (c *cli) showPaths() error {
	fmt.Printf("config:   %s\nbindings: %s\nkeychain: %s\nsessions: %s\n",
		c.paths.ConfigFile, c.paths.Bindings, c.paths.Keychain, c.paths.SessionsDir)
	return nil
}
