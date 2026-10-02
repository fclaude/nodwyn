package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
)

// mcp lists the configured MCP servers and pulls a contained server's
// image (ADR 23 W3b). Pulling is the one step that downloads a server's
// code, so it is a command the person runs, by digest; a task never pulls.
// Servers are added, reviewed and approved in the console's Connections,
// or by editing mcp.toml.
func (c *cli) mcp() error {
	if err := c.rejectGlobals("mcp: put %s after the command"); err != nil {
		return err
	}
	args := c.rest()
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		if len(args) != 0 {
			return fmt.Errorf("mcp list takes no arguments")
		}
		return c.mcpList()
	case "pull":
		if len(args) != 1 {
			return fmt.Errorf("usage: nodwyn mcp pull NAME")
		}
		return c.mcpPull(args[0])
	}
	return fmt.Errorf("mcp: expected list or pull")
}

func (c *cli) mcpList() error {
	servers, err := config.LoadMCP(config.MCPFile(c.paths))
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		fmt.Printf("No MCP servers in %s.\n", config.MCPFile(c.paths))
		return nil
	}
	cfg, err := config.Load(c.paths.ConfigFile)
	if err != nil {
		return err
	}
	rt, rtErr := core.ContainerRuntime(core.Options{Config: cfg})
	for _, s := range servers {
		state := "not reviewed"
		if s.Manifest != "" {
			state = "approved"
		}
		if !s.Contained() {
			fmt.Printf("%-16s remote     %s  (%s)\n", s.Name, s.URL, state)
			continue
		}
		network := "none"
		if len(s.Network) > 0 {
			network = strings.Join(s.Network, ", ")
		}
		image := "image unknown: " + fmt.Sprint(rtErr)
		if rtErr == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			present, err := rt.ImagePresent(ctx, s.Image)
			cancel()
			switch {
			case err != nil:
				image = "image unknown: " + err.Error()
			case present:
				image = "image present"
			default:
				image = "image not pulled: nodwyn mcp pull " + s.Name
			}
		}
		fmt.Printf("%-16s contained  %s  (%s; network: %s; %s)\n", s.Name, s.Image, state, network, image)
	}
	return nil
}

func (c *cli) mcpPull(name string) error {
	servers, err := config.LoadMCP(config.MCPFile(c.paths))
	if err != nil {
		return err
	}
	var server *config.MCPServer
	for i := range servers {
		if servers[i].Name == name {
			server = &servers[i]
		}
	}
	switch {
	case server == nil:
		return fmt.Errorf("no MCP server named %s in %s", name, config.MCPFile(c.paths))
	case !server.Contained():
		return fmt.Errorf("%s is a remote server: nothing to pull", name)
	}
	cfg, err := config.Load(c.paths.ConfigFile)
	if err != nil {
		return err
	}
	rt, err := core.ContainerRuntime(core.Options{Config: cfg})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Printf("Pulling %s with %s…\n", server.Image, rt.Kind)
	if err := rt.Pull(ctx, server.Image, os.Stdout); err != nil {
		return err
	}
	if present, err := rt.ImagePresent(ctx, server.Image); err != nil || !present {
		return fmt.Errorf("%s pulled, but %s does not show it by its digest", server.Image, rt.Kind)
	}
	fmt.Printf("%s is on this computer. Review its tools in Connections before nodwyn offers any.\n", name)
	return nil
}
