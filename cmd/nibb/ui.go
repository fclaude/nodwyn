package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/fclaude/nibb/internal/frontend/rpc"
	webui "github.com/fclaude/nibb/internal/frontend/web"
)

func runUI(args []string) error {
	flags := flag.NewFlagSet("nibb ui", flag.ContinueOnError)
	socket := flags.String("socket", "", "daemon socket path")
	readOnly := flags.Bool("read-only", false, "view sessions without messages, prompt replies, or lifecycle changes")
	noOpen := flags.Bool("no-open", false, "print the local URL without opening a browser")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("ui: unexpected arguments")
	}
	if *socket == "" {
		path, err := rpc.DefaultSocket()
		if err != nil {
			return err
		}
		*socket = path
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	client, err := rpc.Dial(checkCtx, *socket)
	cancel()
	if err != nil {
		return fmt.Errorf("ui: start nibb daemon first (or pass its --socket): %w", err)
	}
	client.Close()
	code, err := webui.PairingCode()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	url := "http://" + listener.Addr().String()
	console := webui.New(webui.Options{Socket: *socket, PairingCode: code, ReadOnly: *readOnly})
	fmt.Fprintln(os.Stderr, "Browser view:", url)
	fmt.Fprintln(os.Stderr, "Pairing code (for another browser on this computer):", code)
	if *readOnly {
		fmt.Fprintln(os.Stderr, "Read-only session view. Ctrl-C stops the bridge; daemon sessions keep running.")
	} else {
		fmt.Fprintln(os.Stderr, "Browser conversation and prompt replies enabled. Ctrl-C stops only the bridge.")
	}
	if !*noOpen {
		command := "xdg-open"
		if runtime.GOOS == "darwin" {
			command = "open"
		}
		// The page this command opens pairs itself with a one-time ticket in
		// the URL fragment, which the browser never sends to a server.
		ticket, err := console.Ticket()
		if err != nil {
			return err
		}
		launcher := exec.CommandContext(ctx, command, url+"/#ticket="+ticket)
		if err := launcher.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "Open the local URL in your browser:", err)
		} else {
			go launcher.Wait()
		}
	}
	return console.Serve(ctx, listener)
}
