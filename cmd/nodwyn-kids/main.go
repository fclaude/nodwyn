// nodwyn-kids is an explicit managed deployment, separate from nodwyn daemon.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/fclaude/nodwyn/internal/frontend/kids"
	"github.com/fclaude/nodwyn/internal/frontend/web"
)

func main() {
	profile := flag.String("profile", "/etc/nodwyn-kids/profile.json", "operator-owned profile")
	invite := flag.String("invite", "", "issue a one-use invitation: parent or child")
	flag.Parse()
	b, err := os.ReadFile(*profile)
	var p kids.Profile
	if err == nil {
		err = json.Unmarshal(b, &p)
	}
	if err == nil && *invite != "" {
		if *invite != "parent" && *invite != "child" {
			err = fmt.Errorf("invite is parent or child")
		} else {
			code, expires, e := web.OpenDevices(filepath.Join(p.State, "devices.toml")).Invite(web.Terminal, *invite == "parent")
			err = e
			if err == nil {
				fmt.Printf("Invitation: %s\nExpires: %s\n", code, expires)
			}
		}
	} else if err == nil {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		err = kids.Run(ctx, p)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
