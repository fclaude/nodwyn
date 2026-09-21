// Command nibb is the entrypoint. It parses flags, resolves configuration,
// and hands a core session to a front-end (ADR 10). It contains no
// behaviour of its own.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/frontend/plain"
	"github.com/fclaude/nibb/internal/scheme"
)

// version is set by the Makefile via -ldflags; "dev" builds fall back to VCS
// info from the build.
var version = "dev"

func main() {
	fs := flag.NewFlagSet("nibb", flag.ExitOnError)
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `usage: nibb [flags] [command]

commands:
  version              print version
  paths                print the config and data locations
  repl                 interactive Scheme REPL (reads stdin)
  eval FILE...         evaluate Scheme files
  eval -e EXPR         evaluate an expression and print its value

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
	switch cmd {
	case "paths":
		p, err := config.DefaultPaths()
		if err != nil {
			fail(err)
		}
		fmt.Printf("config:   %s\nbindings: %s\nkeychain: %s\nsessions: %s\n", p.ConfigFile, p.Bindings, p.Keychain, p.SessionsDir)
	case "repl":
		in := scheme.New(scheme.WithOutput(os.Stdout))
		if err := plain.SchemeREPL(context.Background(), in, os.Stdin, os.Stdout, true); err != nil {
			fail(err)
		}
	case "eval":
		runEval(fs.Args()[1:])
	case "":
		fs.Usage()
		os.Exit(2)
	default:
		fail(fmt.Errorf("unknown command %q", cmd))
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
