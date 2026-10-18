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
	"strconv"
	"strings"
	"sync"
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
	flags.Var(&r.hosts, "name", "another host:port this console answers to, such as a tailnet name (repeatable); https://NAME when a proxy ends TLS in front of it (tailscale serve)")
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
		// Browsers name a host, never 0.0.0.0 or ::, and the console answers
		// only to the names it listens on: a wildcard needs the real ones.
		if host, _, err := net.SplitHostPort(r.listen); err == nil && len(r.hosts) == 0 {
			if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
				return nil, options, fmt.Errorf("--listen %s answers on every interface, but the console only accepts the names it is reached by: add --name HOST:PORT (for example --name desk.local%s), or listen on one address", r.listen, r.listen[strings.LastIndex(r.listen, ":"):])
			}
		}
	}
	var listener net.Listener
	var err error
	if r.listen == "" {
		listener, err = loopbackListener(paths)
	} else {
		listener, err = net.Listen("tcp", address)
	}
	if err != nil {
		return nil, options, err
	}
	if r.cert != "" {
		certificate, err := certificateFiles(r.cert, r.key)
		if err != nil {
			listener.Close()
			return nil, options, err
		}
		listener = tls.NewListener(listener, &tls.Config{GetCertificate: certificate, MinVersion: tls.VersionTLS12})
		options.TLS = true
	}
	options.Hosts = r.hosts
	if r.listen != "" || len(r.hosts) > 0 {
		options.Devices = webui.OpenDevices(devicesFile(paths))
		options.Audit = os.Stderr
		fmt.Fprintln(os.Stderr, "Other devices pair with an invitation: nodwyn devices invite prints one.")
		if options.ReadOnly {
			fmt.Fprintln(os.Stderr, "They can read tasks.")
		} else {
			fmt.Fprintln(os.Stderr, "WARNING: a paired device can read every task, send messages and answer approvals; one signed in")
			fmt.Fprintln(os.Stderr, "with your passkey over https can also change settings and keys. Revoke one with: nodwyn devices revoke NAME")
		}
		if !options.TLS && !allHTTPS(r.hosts) {
			fmt.Fprintln(os.Stderr, "Without --tls-cert traffic is unencrypted unless the network encrypts it (a tailnet does).")
		}
	}
	return listener, options, nil
}

// certificateFiles serves the certificate in certFile and keyFile, read again
// whenever either file changes, so a renewal (certbot, acme.sh, a local CA)
// is served without a restart. A pair that fails to load leaves the one
// served before.
func certificateFiles(certFile, keyFile string) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), error) {
	stamp := func() string {
		var s strings.Builder
		for _, f := range []string{certFile, keyFile} {
			if info, err := os.Stat(f); err == nil {
				fmt.Fprintf(&s, "%d.%d;", info.ModTime().UnixNano(), info.Size())
			}
		}
		return s.String()
	}
	loaded := stamp()
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	return func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		mu.Lock()
		defer mu.Unlock()
		if now := stamp(); now != loaded {
			if renewed, err := tls.LoadX509KeyPair(certFile, keyFile); err == nil {
				pair, loaded = renewed, now
			}
		}
		return &pair, nil
	}, nil
}

// allHTTPS reports whether every name is reached over https through a proxy.
func allHTTPS(hosts names) bool {
	for _, h := range hosts {
		if !strings.HasPrefix(h, "https://") {
			return false
		}
	}
	return len(hosts) > 0
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
		if !strings.HasPrefix(h, "https://") {
			h = scheme + "://" + h
		}
		fmt.Fprintln(os.Stderr, "Also at:", h)
	}
	// A console other machines reach takes no code: devices pair with an
	// invitation, and this computer with the launch ticket below.
	if options.Devices == nil {
		fmt.Fprintln(os.Stderr, "Pairing code (for another browser; keep it off chats and screenshots):", code)
	}
	if *readOnly {
		fmt.Fprintln(os.Stderr, "Read-only session view. Ctrl-C stops the bridge; daemon sessions keep running.")
	} else {
		fmt.Fprintln(os.Stderr, "Browser conversation and prompt replies enabled. Ctrl-C stops only the bridge.")
	}
	// On loopback, whether or not other machines reach it too.
	addr, _ := listener.Addr().(*net.TCPAddr)
	if !*noOpen && addr != nil && addr.IP.IsLoopback() {
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

// devices lists or revokes the browsers paired from other machines, or
// invites one.
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
			signed := ""
			if d.Passkey != "" {
				signed = "  passkey " + d.Passkey
			}
			fmt.Printf("%-20s paired %s  last seen %s%s\n", d.Name, d.Paired.Local().Format("2006-01-02"), d.LastSeen.Local().Format("2006-01-02 15:04"), signed)
		}
		return nil
	case len(args) == 1 && args[0] == "invite":
		// Printed here, to the terminal of whoever ran it, never to the
		// console's log. One from the terminal may register a passkey: it is
		// how the first one is made, and how the person gets in again if
		// every passkey is lost.
		code, expires, err := store.Invite(webui.Terminal, true)
		if err != nil {
			return err
		}
		fmt.Println("Invitation:", code)
		fmt.Printf("Good once, until %s. On the other device, open nodwyn's address, enter it and name the device;\n", expires.Local().Format("15:04"))
		fmt.Println("over https it also makes a passkey there, which other devices can then sign in with.")
		return nil
	case len(args) == 2 && args[0] == "revoke":
		if err := store.Revoke(args[1]); err != nil {
			return err
		}
		fmt.Println("Revoked", args[1]+"; its next request is refused.")
		return nil
	}
	return fmt.Errorf("devices: expected list, invite or revoke NAME")
}

// loopbackListener listens on this computer's loopback address, on the port
// the console had the last time when it is free, else on a new one, which it
// keeps for next time in nodwyn's data directory. A browser keeps what the
// console remembers in it (the theme, thinking shown, folders folded) for
// the console's origin, its port included, and a new port at each launch
// lost all of it. A cookie from an earlier run holds that run's session,
// which this one does not take, so reusing the port pairs nothing.
func loopbackListener(paths config.Paths) (net.Listener, error) {
	file := filepath.Join(paths.DataDir, "console-port")
	if raw, err := os.ReadFile(file); err == nil {
		if port, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && port > 1023 && port < 65536 {
			if l, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
				return l, nil
			}
		}
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if os.MkdirAll(paths.DataDir, 0o700) == nil {
		_ = os.WriteFile(file, []byte(strconv.Itoa(l.Addr().(*net.TCPAddr).Port)+"\n"), 0o600)
	}
	return l, nil
}
