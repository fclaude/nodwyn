// Command nibb is the entrypoint. It parses flags, resolves configuration,
// and hands a core session to a front-end (ADR 10). It contains no
// behaviour of its own: every command is a method on cli, in its own file.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	webui "github.com/fclaude/nibb/internal/frontend/web"
)

// version is set by the Makefile via -ldflags; "dev" builds fall back to VCS
// info from the build.
var version = "dev"

const usage = `usage: nibb [flags] [command]

commands:
  (none) | chat        start a session in the plain front-end
  replay SESSION       print a session log as a transcript (id or path)
  replay SESSION --window SEQ  reconstruct and verify a recorded model request
  summary [flags] SESSION  inspect, add or revoke a conversation summary
  sessions             list sessions
  run [flags]          submit a bounded supervised task to the daemon
  run status|stop|resume [--socket PATH] SESSION
  daemon [--socket PATH] serve local sessions, turns and events
  ui [--socket PATH]    interactive browser view (optional --read-only)
  tui [--socket PATH]   interactive terminal client (local or daemon)
  repl                 interactive Scheme REPL (reads stdin)
  eval FILE...         evaluate Scheme files
  eval -e EXPR         evaluate an expression and print its value
  keychain init        create the encrypted keychain (asks for a passphrase)
  paths                print the config and data locations
  version              print version
  licenses             print the browser console's dependency notices

flags:
`

func main() {
	if err := newCLI(os.Args[1:]).run(); err != nil {
		fail(err)
	}
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
	fmt.Fprintln(os.Stderr, "nibb:", err)
	os.Exit(1)
}
