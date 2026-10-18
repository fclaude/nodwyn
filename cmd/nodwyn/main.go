// Command nodwyn is the entrypoint. It parses flags, resolves configuration,
// and hands a core session to a front-end (ADR 10). It contains no
// behaviour of its own: every command is a method on cli, in its own file.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/fclaude/nodwyn/internal/config"
	// The terminal emulator the experimental terminal primitives use (ADR 48);
	// the core may not import it itself (ADR 10), so the binary links it in.
	_ "github.com/fclaude/nodwyn/internal/frontend/termvt"
	webui "github.com/fclaude/nodwyn/internal/frontend/web"
)

// version is set by the Makefile via -ldflags; "dev" builds fall back to VCS
// info from the build.
var version = "dev"

const usage = `usage: nodwyn [flags] [command]

The flags below go before the command, and apply to the commands that read
them (chat all of them; replay --show-thinking; memory, versions and undo
--repo). A command with flags of its own takes them after its name.

commands:
  (none) | chat        start a task in line mode
  replay SESSION       print a session log as a transcript (id or path)
  replay SESSION --window SEQ  reconstruct and verify a recorded model request
  summary [flags] SESSION  inspect, add or revoke a conversation summary
  memory search [--task SESSION] [-n N] WORDS  search what this repository's tasks did, by turn
  memory recall SESSION SEQ [START [COUNT]]   one event; a turn's first message with what the turn did
  sessions [--ids]     list stored tasks (--ids: just their session ids)
  versions FILE        list a file's kept previous versions (--repo sets the workspace)
  undo FILE            put back a file's newest kept version
  run [flags]          carry a task to its end with no conversation, for scripts and
                       benchmarks (docs/runs.md; --unattended: nobody answers it;
                       --judge: the judge answers its confirms)
  run status|stop|resume [--socket PATH] SESSION  (resume also takes --yolo, --judge, --unattended)
  daemon [--socket PATH] serve local sessions, turns and events
  startup [on|off]     whether the daemon starts at login, so scheduled runs go on
                       with no window open (a systemd user unit, a launchd agent)
  app [--browser]      nodwyn as one program: a window, and a daemon if none runs
  ui [--socket PATH]    interactive browser view (optional --read-only)
  ui --listen ADDR     also serve other machines; devices pair by name
  devices [revoke NAME] list or revoke browsers paired from other machines
  tui [--socket PATH]   interactive terminal client (local or daemon)
  repl                 interactive Scheme REPL (reads stdin)
  eval FILE...         evaluate Scheme files
  eval -e EXPR         evaluate an expression and print its value
  keychain init        create the encrypted keychain (asks for a passphrase;
                       --no-passphrase makes one without: not recommended)
  keychain passphrase  set or change its passphrase (--remove: none, not recommended)
  credential [list]    list the keychain's credentials (never a secret)
  credential add --oauth2 PROVIDER --client-id ID [--scopes A,B] [--class CLASS]
                       authorize an OAuth2 credential in the browser (google,
                       microsoft, github, notion, or custom with --auth-url/--token-url)
  credential login ID|CLASS    authorize an OAuth2 credential again
  credential revoke [--local] ID|CLASS  revoke at the provider and delete here
  mcp [list]           list the MCP servers, and whether a contained one's image is here
  mcp pull NAME        download a contained MCP server's image, by its digest
  trust        review, grant or revoke repository startup trust
  paths                print the config and data locations
  version              print version
  licenses             print the browser console's dependency notices

flags:
`

func main() {
	args := os.Args[1:]
	// Finder and the Dock start a bundle's executable with no arguments:
	// that is the app.
	if launchedFromBundle(args) {
		if paths, err := defaultPaths(); err == nil {
			prepareBundleLaunch(paths)
		}
		args = []string{"app"}
	}
	if err := newCLI(args).run(); err != nil {
		fail(err)
	}
}

// defaultPaths resolves the config and data directories, first moving
// ones kept under nodwyn's former name (ADR 31).
func defaultPaths() (config.Paths, error) {
	paths, err := config.DefaultPaths()
	if err != nil {
		return config.Paths{}, err
	}
	moved, err := config.MigrateLegacy(paths)
	for _, dir := range moved {
		fmt.Fprintf(os.Stderr, "nodwyn: moved %s to %s (nodwyn was called %s)\n", filepath.Join(filepath.Dir(dir), config.LegacyName), dir, config.LegacyName)
	}
	if err != nil {
		return config.Paths{}, fmt.Errorf("moving %s's folders: %w", config.LegacyName, err)
	}
	return paths, nil
}

func licenseText() string { return webui.Licenses() }

func versionString() string {
	if version != "dev" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
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
	if rev == "" {
		return version
	}
	return "dev-" + rev + dirty
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "nodwyn:", err)
	os.Exit(1)
}
