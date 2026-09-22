package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/frontend/rpc"
)

func runDaemon(paths config.Paths, args []string, noKeychain bool) error {
	flags := flag.NewFlagSet("nibb daemon", flag.ContinueOnError)
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
	options := core.Options{Config: cfg, Paths: paths, Version: versionString()}
	if !noKeychain {
		options.Keychain, options.Bindings = openKeychain(paths)
	}
	listener, err := rpc.Listen(*socket)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintln(os.Stderr, "daemon: listening on", *socket)
	return rpc.Serve(ctx, listener, options)
}
