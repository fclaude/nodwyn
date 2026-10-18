package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/frontend/workflows"
)

// workflows lists the daemon's workflows, and pauses, resumes or checks one:
//
//	nodwyn workflows [list]
//	nodwyn workflows pause|resume|check|run REF
//
// REF is the reference in brackets in the list, or the end of it. It lists the
// timetables (scheduled tasks) and the workflows that watch a source, as the
// console's Workflows page does, and acts on either (check polls a source, run
// runs a timetable now). A workflow is made in the console (ADR 53).
func (c *cli) workflows() error {
	if err := c.rejectGlobals("workflows: put --%s after workflows"); err != nil {
		return err
	}
	return workflowsCommand(c.rest(), "workflows", func(ctx context.Context, client *rpc.Client, args []string) error {
		listing, err := workflows.Read(ctx, client)
		if err != nil {
			return err
		}
		switch {
		case len(args) == 0 || (len(args) == 1 && args[0] == "list"):
			fmt.Print(listing.Workflows())
			return nil
		case len(args) == 2 && (args[0] == "pause" || args[0] == "resume" || args[0] == "check" || args[0] == "run"):
			msg, err := listing.Act(ctx, client, args[1], args[0])
			if err != nil {
				return err
			}
			fmt.Println(msg)
			return nil
		}
		return fmt.Errorf("workflows: use nodwyn workflows, or nodwyn workflows pause|resume|check|run REF")
	})
}

// outbox lists the answers workflows hold for the person to post, and posts or
// discards one:
//
//	nodwyn outbox [list]
//	nodwyn outbox post|discard REF
//
// The daemon posts, with a key bound for exactly that call; the words are the
// run's (change them in the console).
func (c *cli) outbox() error {
	if err := c.rejectGlobals("outbox: put --%s after outbox"); err != nil {
		return err
	}
	return workflowsCommand(c.rest(), "outbox", func(ctx context.Context, client *rpc.Client, args []string) error {
		listing, err := workflows.Read(ctx, client)
		if err != nil {
			return err
		}
		switch {
		case len(args) == 0 || (len(args) == 1 && args[0] == "list"):
			fmt.Print(listing.HeldAnswers())
			return nil
		case len(args) == 2 && (args[0] == "post" || args[0] == "discard"):
			msg, err := listing.Decide(ctx, client, args[1], args[0])
			if err != nil {
				return err
			}
			fmt.Println(msg)
			return nil
		}
		return fmt.Errorf("outbox: use nodwyn outbox, or nodwyn outbox post|discard REF")
	})
}

// workflowsCommand connects to the running daemon (--socket PATH, else the
// default) and runs do with the arguments left after the flag.
func workflowsCommand(args []string, name string, do func(context.Context, *rpc.Client, []string) error) error {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	socket := flags.String("socket", "", "the daemon's Unix socket")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *socket == "" {
		path, err := rpc.DefaultSocket()
		if err != nil {
			return err
		}
		*socket = path
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client, err := rpc.Dial(ctx, *socket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s needs a running nodwyn daemon: nodwyn app, nodwyn tui or nodwyn daemon starts one\n", name)
		return err
	}
	defer client.Close()
	return do(ctx, client, flags.Args())
}
