package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
)

func runDaemon(paths config.Paths, args []string, noKeychain bool) error {
	flags := flag.NewFlagSet("nodwyn daemon", flag.ContinueOnError)
	flags.BoolVar(&noKeychain, "no-keychain", noKeychain, "start without opening the keychain")
	socket := flags.String("socket", "", "absolute socket path (parent must be private, mode 0700)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("daemon: unexpected arguments")
	}
	if *socket == "" {
		path, err := rpc.DefaultSocket()
		if err != nil {
			return err
		}
		*socket = path
	}
	cfg, err := config.Load(paths.ConfigFile)
	if err != nil {
		return err
	}
	// The daemon's tasks are listed by name, so it asks the model to name
	// each from its first message (ADR 39, amended 2026-10-23).
	options := core.Options{Config: cfg, Paths: paths, Version: versionString(), Titles: true}
	if !noKeychain {
		if options.Keychain, options.Bindings, err = openKeychain(paths, false); err != nil {
			return err
		}
	}
	listener, err := rpc.Listen(*socket)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintln(os.Stderr, "daemon: listening on", *socket)
	return rpc.Serve(ctx, listener, options, daemonOptions()...)
}
