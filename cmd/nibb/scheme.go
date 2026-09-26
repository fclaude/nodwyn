package main

import (
	"context"
	"fmt"
	"os"

	"github.com/fclaude/nibb/internal/frontend/plain"
	"github.com/fclaude/nibb/internal/scheme"
)

// repl evaluates Scheme from stdin. It has no host primitives and no
// guard: there is no session, so there is nothing to reach.
func (c *cli) repl() error {
	in := scheme.New(scheme.WithOutput(os.Stdout))
	return plain.SchemeREPL(context.Background(), in, os.Stdin, os.Stdout, true)
}

// eval runs Scheme files, or a single expression with -e.
func (c *cli) eval() error {
	args := c.rest()
	in := scheme.New(scheme.WithOutput(os.Stdout))
	ctx := context.Background()
	if len(args) >= 2 && args[0] == "-e" {
		value, err := in.EvalString(ctx, args[1])
		if err != nil {
			return err
		}
		if value.Kind != scheme.KVoid {
			fmt.Println(scheme.Write(value))
		}
		return nil
	}
	if len(args) == 0 {
		return fmt.Errorf("eval: expected files or -e EXPR")
	}
	for _, path := range args {
		if err := in.LoadFile(ctx, path); err != nil {
			return err
		}
	}
	return nil
}
