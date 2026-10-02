// Command stdio is a fake MCP server over stdio for the opt-in tests that
// run a contained server with a real container runtime (ADR 23 W3b). Built
// static for Linux, it runs in any image, and its tools probe the
// isolation from inside:
//
//	fetch    an HTTP GET through the environment's proxy, as most clients do
//	dial     a TCP connection that ignores any proxy
//	resolve  a DNS lookup
//	env      whether a variable is set (never its value)
//	write    a file
//
// Every tool answers in text, an error included, so a probe that fails is
// a result and not a failed call.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	fakemcp "github.com/fclaude/nodwyn/test/fakes/mcp"
)

func arg(args map[string]any, name string) string { s, _ := args[name].(string); return s }

func schema(names ...string) map[string]any {
	props := map[string]any{}
	for _, n := range names {
		props[n] = map[string]any{"type": "string"}
	}
	return map[string]any{"type": "object", "properties": props, "required": names}
}

func main() {
	tools := []fakemcp.Tool{
		{Name: "fetch", Description: "GET a URL through the proxy", Schema: schema("url"), Reply: func(args map[string]any) (string, bool) {
			client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
			resp, err := client.Get(arg(args, "url"))
			if err != nil {
				return "error: " + err.Error(), false
			}
			resp.Body.Close()
			return fmt.Sprintf("status %d", resp.StatusCode), false
		}},
		{Name: "dial", Description: "Connect directly, ignoring any proxy", Schema: schema("address"), Reply: func(args map[string]any) (string, bool) {
			conn, err := net.DialTimeout("tcp", arg(args, "address"), 5*time.Second)
			if err != nil {
				return "error: " + err.Error(), false
			}
			conn.Close()
			return "connected", false
		}},
		{Name: "resolve", Description: "Look up a name", Schema: schema("host"), Reply: func(args map[string]any) (string, bool) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			addrs, err := net.DefaultResolver.LookupHost(ctx, arg(args, "host"))
			if err != nil {
				return "error: " + err.Error(), false
			}
			return "resolved " + strings.Join(addrs, " "), false
		}},
		{Name: "env", Description: "Whether a variable is set", Schema: schema("name"), Reply: func(args map[string]any) (string, bool) {
			if _, ok := os.LookupEnv(arg(args, "name")); ok {
				return "set", false
			}
			return "unset", false
		}},
		{Name: "write", Description: "Write a file", Schema: schema("path", "text"), Reply: func(args map[string]any) (string, bool) {
			if err := os.WriteFile(arg(args, "path"), []byte(arg(args, "text")), 0o644); err != nil {
				return "error: " + err.Error(), false
			}
			return "written", false
		}},
	}
	if err := fakemcp.NewStdio(tools...).Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
