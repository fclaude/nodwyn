package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/frontend/rpc"
	webui "github.com/fclaude/nibb/internal/frontend/web"
)

// app is nibb as one program (ADR 30): it attaches to a daemon already
// answering on the socket, or owns one in this process, and shows the
// console in a window of its own (with the webview build tag) or in the
// default browser. No terminal is needed: the keychain is unlocked and the
// model chosen from the console.
func (c *cli) app() error {
	if err := c.rejectGlobals("app: put %s after the command"); err != nil {
		return err
	}
	flags := flag.NewFlagSet("nibb app", flag.ContinueOnError)
	socket := flags.String("socket", "", "daemon socket path (default: the standard one)")
	browser := flags.Bool("browser", false, "use the default browser even when a window is available")
	noOpen := flags.Bool("no-open", false, "print the console's launch address instead of opening it")
	if err := flags.Parse(c.rest()); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("app: unexpected arguments")
	}
	return runApp(c.paths, *socket, *browser, *noOpen)
}

func runApp(paths config.Paths, socket string, forceBrowser, noOpen bool) error {
	if socket == "" {
		path, err := rpc.DefaultSocket()
		if err != nil {
			return err
		}
		socket = path
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	owned, daemonDone, err := ensureDaemon(ctx, paths, socket)
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
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	console := webui.New(webui.Options{Socket: socket, PairingCode: code})
	ticket, err := console.Ticket()
	if err != nil {
		return err
	}
	base := "http://" + listener.Addr().String()
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
// in this process. A socket file nobody answers on is left by a crash: it
// is removed only after the dial proved no daemon owns it.
func ensureDaemon(ctx context.Context, paths config.Paths, socket string) (owned bool, done chan error, err error) {
	dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	client, dialErr := rpc.Dial(dialCtx, socket)
	cancel()
	if dialErr == nil {
		client.Close()
		return false, nil, nil
	}
	if info, statErr := os.Lstat(socket); statErr == nil && info.Mode()&os.ModeSocket != 0 {
		if err := os.Remove(socket); err != nil {
			return false, nil, fmt.Errorf("app: stale socket %s: %w", socket, err)
		}
	}
	cfg, err := config.Load(paths.ConfigFile)
	if err != nil {
		return false, nil, err
	}
	listener, err := rpc.Listen(socket)
	if err != nil {
		return false, nil, err
	}
	// The keychain starts locked: the console asks for the passphrase.
	options := core.Options{Config: cfg, Paths: paths, Version: versionString()}
	done = make(chan error, 1)
	go func() { done <- rpc.Serve(ctx, listener, options) }()
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
			fmt.Fprintf(f, "\n--- %s nibb %s\n", time.Now().Format(time.RFC3339), versionString())
			os.Stderr = f
		}
	}
}
