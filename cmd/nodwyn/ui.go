package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	webui "github.com/fclaude/nodwyn/internal/frontend/web"
)

// names collects a repeatable flag.
type names []string

func (n *names) String() string     { return strings.Join(*n, ",") }
func (n *names) Set(v string) error { *n = append(*n, v); return nil }

// remoteFlags are the options that make a console reachable from other
// machines (ADR 30); nodwyn ui and nodwyn app share them.
type remoteFlags struct {
	listen, cert, key string
	hosts             names
}

func (r *remoteFlags) register(flags *flag.FlagSet) {
	flags.StringVar(&r.listen, "listen", "", "also serve other machines at this address, e.g. a tailnet IP:7777 (devices pair by name)")
	flags.Var(&r.hosts, "name", "another host:port this console answers to, such as a tailnet name (repeatable)")
	flags.StringVar(&r.cert, "tls-cert", "", "serve https with this certificate (with --tls-key)")
	flags.StringVar(&r.key, "tls-key", "", "the certificate's private key")
}

// listener opens the console's listener: loopback on a random port, or the
// --listen address. It returns the options that go with it.
func (r *remoteFlags) listener(paths config.Paths, options webui.Options) (net.Listener, webui.Options, error) {
	if (r.cert == "") != (r.key == "") {
		return nil, options, fmt.Errorf("--tls-cert and --tls-key go together")
	}
	address := "127.0.0.1:0"
	if r.listen != "" {
		address = r.listen
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, options, err
	}
	if r.cert != "" {
		pair, err := tls.LoadX509KeyPair(r.cert, r.key)
		if err != nil {
			listener.Close()
			return nil, options, err
		}
		listener = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
		options.TLS = true
	}
	options.Hosts = r.hosts
	if r.listen != "" || len(r.hosts) > 0 {
		options.Devices = webui.OpenDevices(devicesFile(paths))
		if options.ReadOnly {
			fmt.Fprintln(os.Stderr, "Other devices can pair by name and read tasks.")
		} else {
			fmt.Fprintln(os.Stderr, "WARNING: a device paired with the code can read every task, send messages and answer approvals")
			fmt.Fprintln(os.Stderr, "(never settings, keys or policy files). Revoke one with: nodwyn devices revoke NAME")
		}
		if !options.TLS {
			fmt.Fprintln(os.Stderr, "Without --tls-cert traffic is unencrypted unless the network encrypts it (a tailnet does).")
		}
	}
	return listener, options, nil
}

func devicesFile(paths config.Paths) string { return filepath.Join(paths.ConfigDir, "devices.toml") }

func runUI(paths config.Paths, args []string) error {
	flags := flag.NewFlagSet("nodwyn ui", flag.ContinueOnError)
	socket := flags.String("socket", "", "daemon socket path")
	readOnly := flags.Bool("read-only", false, "view sessions without messages, prompt replies, or lifecycle changes")
	noOpen := flags.Bool("no-open", false, "print the local URL without opening a browser")
	var remote remoteFlags
	remote.register(flags)
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
		return fmt.Errorf("ui: start nodwyn daemon first (or pass its --socket): %w", err)
	}
	client.Close()
	code, err := webui.PairingCode()
	if err != nil {
		return err
	}
	listener, options, err := remote.listener(paths, webui.Options{Socket: *socket, PairingCode: code, ReadOnly: *readOnly})
	if err != nil {
		return err
	}
	defer listener.Close()
	scheme := "http"
	if options.TLS {
		scheme = "https"
	}
	url := scheme + "://" + listener.Addr().String()
	console := webui.New(options)
	fmt.Fprintln(os.Stderr, "Browser view:", url)
	for _, h := range options.Hosts {
		fmt.Fprintln(os.Stderr, "Also at:", scheme+"://"+h)
	}
	fmt.Fprintln(os.Stderr, "Pairing code (for another browser; keep it off chats and screenshots):", code)
	if *readOnly {
		fmt.Fprintln(os.Stderr, "Read-only session view. Ctrl-C stops the bridge; daemon sessions keep running.")
	} else {
		fmt.Fprintln(os.Stderr, "Browser conversation and prompt replies enabled. Ctrl-C stops only the bridge.")
	}
	if !*noOpen && remote.listen == "" {
		// The page this command opens pairs itself with a one-time ticket in
		// the URL fragment, which the browser never sends to a server.
		ticket, err := console.Ticket()
		if err != nil {
			return err
		}
		if err := openBrowser(ctx, url+"/#ticket="+ticket); err != nil {
			fmt.Fprintln(os.Stderr, "Open the local URL in your browser:", err)
		}
	}
	return console.Serve(ctx, listener)
}

// devices lists or revokes the browsers paired from other machines.
func (c *cli) devices() error {
	if err := c.rejectGlobals("devices: put %s after the command"); err != nil {
		return err
	}
	store := webui.OpenDevices(devicesFile(c.paths))
	args := c.rest()
	switch {
	case len(args) == 0 || (len(args) == 1 && args[0] == "list"):
		list, err := store.List()
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("No devices are paired.")
		}
		for _, d := range list {
			fmt.Printf("%-20s paired %s  last seen %s\n", d.Name, d.Paired.Local().Format("2006-01-02"), d.LastSeen.Local().Format("2006-01-02 15:04"))
		}
		return nil
	case len(args) == 2 && args[0] == "revoke":
		if err := store.Revoke(args[1]); err != nil {
			return err
		}
		fmt.Println("Revoked", args[1]+"; its next request is refused.")
		return nil
	}
	return fmt.Errorf("devices: expected list or revoke NAME")
}
