// Command nibb is the entrypoint. It parses flags, resolves configuration,
// and hands a core session to a front-end (ADR 10). It contains no
// behaviour of its own.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/fclaude/nibb/internal/config"
)

// version is set by the Makefile via -ldflags; "dev" builds fall back to VCS
// info from the build.
var version = "dev"

func main() {
	fs := flag.NewFlagSet("nibb", flag.ExitOnError)
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: nibb [flags] [command]\n\ncommands:\n  version     print version\n  paths       print the config and data locations\n\nflags:\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(os.Args[1:])

	cmd := fs.Arg(0)
	if *showVersion || cmd == "version" {
		fmt.Println("nibb", versionString())
		return
	}
	switch cmd {
	case "paths":
		p, err := config.DefaultPaths()
		if err != nil {
			fail(err)
		}
		fmt.Printf("config:   %s\nbindings: %s\nkeychain: %s\nsessions: %s\n", p.ConfigFile, p.Bindings, p.Keychain, p.SessionsDir)
	case "":
		fs.Usage()
		os.Exit(2)
	default:
		fail(fmt.Errorf("unknown command %q", cmd))
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
