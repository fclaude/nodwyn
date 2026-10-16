package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/plain"
)

// memoryCommand searches a repository's tasks and recalls their events
// from a terminal, as index-search with scope "earlier" and recall do for
// the model (ADR 50, ADR 51): the repository is --repo's, or the current
// folder's. The person's switch for earlier tasks decides what the model
// reads; this is a person reading their own logs.
func memoryCommand(paths config.Paths, repo string, args []string) error {
	const usage = "usage: nodwyn [--repo DIR] memory search [--task SESSION] [-n N] WORDS | memory recall SESSION SEQ [START [COUNT]]"
	if len(args) == 0 || (args[0] != "search" && args[0] != "recall") {
		return fmt.Errorf("%s", usage)
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return err
	}
	project := config.CanonicalPath(abs)
	store := core.Store{Dir: paths.SessionsDir}
	if args[0] == "recall" {
		if len(args) < 3 || len(args) > 5 {
			return fmt.Errorf("%s", usage)
		}
		var nums [3]uint64
		for i, arg := range args[2:] {
			if nums[i], err = strconv.ParseUint(arg, 10, 64); err != nil || (i == 0 && nums[i] == 0) {
				return fmt.Errorf("%s: %q is not a seq or a line count", usage, arg)
			}
		}
		text, err := store.RecallTask(project, args[1], nums[0], int64(nums[1]), int64(nums[2]))
		if err != nil {
			return err
		}
		fmt.Fprintln(plain.TerminalText(os.Stdout), text)
		return nil
	}
	flags := flag.NewFlagSet("memory search", flag.ContinueOnError)
	task := flags.String("task", "", "search only this session")
	limit := flags.Int("n", 8, "how many events to show, at most 20")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	query := strings.Join(flags.Args(), " ")
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("%s", usage)
	}
	text, err := store.SearchTasks(project, *task, query, min(max(*limit, 1), 20))
	if err != nil {
		return err
	}
	fmt.Fprint(plain.TerminalText(os.Stdout), text)
	return nil
}
