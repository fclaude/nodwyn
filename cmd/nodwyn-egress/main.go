// Command nodwyn-egress is the allowlisting proxy of a contained MCP server
// (ADR 23 W3b, internal/egress). nodwyn runs it in a sidecar container,
// mounted read-only as the entrypoint; it is built static and for Linux so
// it runs in whatever image the server uses:
//
//	CGO_ENABLED=0 GOOS=linux go build -o bin/nodwyn-egress ./cmd/nodwyn-egress
//	nodwyn-egress -listen :3128 -allow api.notion.com -allow uploads.example.com:8443
//
// It writes one JSON line per attempt on stdout, the first saying where it
// listens, and stops on SIGTERM or SIGINT, or with -lifeline when its
// standard input ends: nodwyn holds that open, so the proxy does not
// outlive it.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/fclaude/nodwyn/internal/egress"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	listen := flag.String("listen", ":3128", "address to accept proxy connections on")
	var allow list
	flag.Var(&allow, "allow", "a host (ports 443 and 80) or host:port the server may reach; repeat for more")
	lifeline := flag.Bool("lifeline", false, "stop when standard input ends")
	flag.Parse()
	proxy, err := egress.New(allow, egress.Lines(os.Stdout))
	if err != nil {
		fail(err)
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *lifeline {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			stop()
		}()
	}
	if err := proxy.Serve(ctx, l); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "nodwyn-egress:", err)
	os.Exit(1)
}
