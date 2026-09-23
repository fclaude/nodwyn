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
	fmt.Fprintln(os.Stderr, "Browser view:", url)
	fmt.Fprintln(os.Stderr, "Pairing code (paste into this browser only):", code)
	fmt.Fprintln(os.Stderr, "Read-only session view. Ctrl-C stops the browser bridge; daemon sessions keep running.")
	if !*noOpen {
		command := "xdg-open"
		if runtime.GOOS == "darwin" {
			command = "open"
		}
		launcher := exec.CommandContext(ctx, command, url)
		if err := launcher.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "Open the local URL in your browser:", err)
		} else {
			go launcher.Wait()
		}
	}
	return webui.Serve(ctx, listener, *socket, code)
}
