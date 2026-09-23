package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/window"
)

func summaryCommand(paths config.Paths, args []string) error {
	flags := flag.NewFlagSet("summary", flag.ContinueOnError)
	from := flags.Uint64("from", 0, "first user.msg sequence in the span")
	through := flags.Uint64("through", 0, "last completed llm.resp sequence in the span")
	file := flags.String("text", "", "file containing user-authored summary text (maximum 16 KiB)")
	drop := flags.Uint64("drop", 0, "summary event sequence to revoke")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("usage: nibb summary [--from N --through N --text FILE | --drop N] SESSION")
	}
	id := flags.Arg(0)
	store := core.Store{Dir: paths.SessionsDir}
	dropSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "drop" {
			dropSet = true
		}
	})
	if dropSet {
		if flags.NFlag() != 1 {
			return fmt.Errorf("summary: --drop cannot be combined with creation options")
		}
		ev, err := store.DropSummary(id, *drop)
		if err != nil {
			return err
		}
		fmt.Printf("summary %d revoked at seq %d; future windows recompute from the log\n", *drop, ev.Seq)
		return nil
	}
	if flags.NFlag() != 0 {
		if *from == 0 || *through == 0 || *file == "" {
			return fmt.Errorf("summary: --from, --through and --text are all required")
		}
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer f.Close()
		text, err := io.ReadAll(io.LimitReader(f, window.MaxSummaryBytes+1))
		if err != nil {
			return err
		}
		ev, err := store.AddSummary(id, *from, *through, string(text))
		if err != nil {
			return err
		}
		fmt.Printf("summary recorded at seq %d; the latest two user turns stay outside summary replacement\n", ev.Seq)
		return nil
	}
	views, err := store.Summaries(id)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(views)
}
