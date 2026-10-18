package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	webui "github.com/fclaude/nodwyn/internal/frontend/web"
	"github.com/fclaude/nodwyn/internal/keychain"
)

// app is nodwyn as one program (ADR 30): it attaches to a daemon already
// answering on the socket, or owns one in this process, and shows the
// console in a window of its own (with the webview build tag) or in the
// default browser. No terminal is needed: the keychain is unlocked (or
// opens by itself, if it has no passphrase) and the model chosen from the
// console.
func (c *cli) app() error {
	if err := c.rejectGlobals("app: put %s after the command"); err != nil {
		return err
	}
	flags := flag.NewFlagSet("nodwyn app", flag.ContinueOnError)
	socket := flags.String("socket", "", "daemon socket path (default: the standard one)")
	browser := flags.Bool("browser", false, "use the default browser even when a window is available")
	noOpen := flags.Bool("no-open", false, "print the console's launch address instead of opening it")
	var remote remoteFlags
	remote.register(flags)
	if err := flags.Parse(c.rest()); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("app: unexpected arguments")
	}
	return runApp(c.paths, *socket, *browser, *noOpen, remote)
}

func runApp(paths config.Paths, socket string, forceBrowser, noOpen bool, remote remoteFlags) error {
	if socket == "" {
		path, err := rpc.DefaultSocket()
		if err != nil {
			return err
		}
		socket = path
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	owned, daemonDone, err := ensureDaemon(ctx, paths, socket, "app")
	if err != nil {
		return err
	}
	if owned {
		fmt.Fprintln(os.Stderr, "app: serving tasks in this process on", socket)
	} else {
		fmt.Fprintln(os.Stderr, "app: using the daemon already running on", socket)
	}

	code, err := webui.PairingCode()
	if err != nil {
		return err
	}
	// The window always uses its own loopback console; --listen adds a
	// second one for other devices.
	listener, err := loopbackListener(paths)
	if err != nil {
		return err
	}
	console := webui.New(webui.Options{Socket: socket, PairingCode: code})
	ticket, err := console.Ticket()
	if err != nil {
		return err
	}
	base := "http://" + listener.Addr().String()
	if remote.listen != "" || len(remote.hosts) > 0 {
		shared, options, err := remote.listener(paths, webui.Options{Socket: socket, PairingCode: code})
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "app: other devices can pair at", shared.Addr().String(), "with code", code)
		go func() { _ = webui.New(options).Serve(ctx, shared) }()
	}
	fmt.Fprintln(os.Stderr, "app: console at", base, "(pairing code for another browser here:", code+")")
	webCtx, stopWeb := context.WithCancel(ctx)
	webDone := make(chan error, 1)
	go func() { webDone <- console.Serve(webCtx, listener) }()

	// The window owns the main thread until it closes; closing it ends the
	// app, and with it the tasks this process owns (their logs remain).
	var windowErr error
	if noOpen {
		fmt.Fprintln(os.Stderr, "app: open", base+"/#ticket="+ticket)
		<-ctx.Done()
	} else {
		windowErr = openWindow(ctx, base+"/#ticket="+ticket, forceBrowser)
	}
	stopWeb()
	stop()
	err = errors.Join(windowErr, <-webDone)
	if daemonDone != nil {
		err = errors.Join(err, <-daemonDone)
	}
	return err
}

// ensureDaemon attaches to a daemon that answers on socket, or starts one
// in this process, for nodwyn app, nodwyn tui and line mode (command, which
// its messages begin with). rpc.Listen removes a socket file nobody listens
// on, which a crash leaves. A daemon that listens but cannot be used (an
// older protocol, a hello it refuses, a slow answer) is still running its
// tasks and schedules: its socket stays, and the command says so instead of
// starting a second daemon beside it.
func ensureDaemon(ctx context.Context, paths config.Paths, socket, command string) (owned bool, done chan error, err error) {
	attach := func() error {
		dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		client, err := rpc.Dial(dialCtx, socket)
		if err == nil {
			client.Close()
		}
		return err
	}
	dialErr := attach()
	if dialErr == nil {
		return false, nil, nil
	}
	// Listening before the configuration is read refuses a daemon this build
	// cannot use before anything else can fail.
	listener, err := rpc.Listen(socket)
	if errors.Is(err, rpc.ErrInUse) {
		// A daemon may have started since the dial above.
		if dialErr = attach(); dialErr == nil {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("%s: a daemon is running on %s, but this build cannot use it (%w); stop it, or run the nodwyn it came with", command, socket, dialErr)
	}
	if err != nil {
		return false, nil, err
	}
	cfg, err := config.Load(paths.ConfigFile)
	if err != nil {
		listener.Close()
		return false, nil, err
	}
	// The keychain starts locked, and the console, the TUI or line mode asks
	// for the passphrase; one without a passphrase is opened now.
	options := core.Options{Config: cfg, Paths: paths, Version: versionString()}
	if store, err := keychain.OpenWithoutPassphrase(paths.Keychain); err == nil {
		if options.Bindings, err = binding.Load(paths.Bindings); err != nil {
			listener.Close()
			return false, nil, err
		}
		options.Keychain = store
		fmt.Fprintln(os.Stderr, command+": the keychain has no passphrase; opened it")
	}
	done = make(chan error, 1)
	go func() { done <- rpc.Serve(ctx, listener, options, daemonOptions()...) }()
	return true, done, nil
}

// openBrowser shows url in the default browser.
func openBrowser(ctx context.Context, url string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	launcher := exec.CommandContext(ctx, command, url)
	if err := launcher.Start(); err != nil {
		return err
	}
	go launcher.Wait()
	return nil
}

// launchedFromBundle reports whether this binary runs as a macOS app
// bundle's executable, as Finder or the Dock starts it: no arguments, and
// a path inside Something.app/Contents/MacOS.
func launchedFromBundle(args []string) bool {
	if len(args) != 0 || runtime.GOOS != "darwin" {
		return false
	}
	exe, err := os.Executable()
	return err == nil && strings.Contains(exe, ".app/Contents/MacOS/")
}

// prepareBundleLaunch gives a Finder-launched app what a terminal would:
// the login shell's PATH (so tools installed with Homebrew are found), the
// home directory as working directory, and a log file for diagnostics.
func prepareBundleLaunch(paths config.Paths) {
	if shell := os.Getenv("SHELL"); shell != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, err := exec.CommandContext(ctx, shell, "-l", "-c", "printf %s \"$PATH\"").Output()
		cancel()
		if err == nil && len(out) > 0 && !strings.ContainsAny(string(out), "\n\x00") {
			os.Setenv("PATH", string(out))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		_ = os.Chdir(home)
	}
	if err := os.MkdirAll(paths.DataDir, 0o700); err == nil {
		if f, err := os.OpenFile(filepath.Join(paths.DataDir, "app.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			fmt.Fprintf(f, "\n--- %s nodwyn %s\n", time.Now().Format(time.RFC3339), versionString())
			os.Stderr = f
		}
	}
}
