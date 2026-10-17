package main

import (
	"context"
	"fmt"

	"github.com/fclaude/nodwyn/internal/prim"
)

// outline, symbol and tests-for print what the tools of the same names give
// the model, for a shell to run (internal/prim/codeindex_cli.go).

func (c *cli) outline() error {
	args := c.rest()
	if len(args) != 1 {
		return fmt.Errorf("outline: expected one file, as in: outline internal/prim/fs.go")
	}
	text, err := prim.OutlineCommand(context.Background(), args[0])
	if err != nil {
		return err
	}
	fmt.Print(text)
	return nil
}

func (c *cli) symbol() error {
	args := c.rest()
	if len(args) != 2 {
		return fmt.Errorf("symbol: expected a file and a definition, as in: symbol internal/prim/fs.go fsRead")
	}
	text, err := prim.SymbolCommand(context.Background(), args[0], args[1])
	if err != nil {
		return err
	}
	fmt.Print(text)
	return nil
}

func (c *cli) testsFor() error {
	args := c.rest()
	if len(args) != 1 {
		return fmt.Errorf("tests-for: expected one source file, as in: tests-for internal/prim/fs.go")
	}
	text, err := prim.TestsForCommand(context.Background(), args[0])
	if err != nil {
		return err
	}
	fmt.Print(text)
	return nil
}
