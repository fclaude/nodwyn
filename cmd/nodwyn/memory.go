package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/memory"
)

// memoryCommand searches and reads the memory of a repository's tasks (ADR
// 50) from a terminal, as the model's memory-search and memory-read do in a
// session: the repository is --repo's, or the current folder's.
func memoryCommand(paths config.Paths, repo string, args []string) error {
	if len(args) == 0 || (args[0] != "search" && args[0] != "read") {
		return fmt.Errorf("usage: nodwyn [--repo DIR] memory search [--task SESSION] [--exact] WORDS | memory read ID")
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return err
	}
	project := config.CanonicalPath(abs)
	store := core.Store{Dir: paths.SessionsDir}
	if args[0] == "read" {
		if len(args) != 2 {
			return fmt.Errorf("usage: nodwyn [--repo DIR] memory read ID")
		}
		text, err := store.ReadMemory(project, args[1])
		if err != nil {
			return err
		}
		fmt.Print(text)
		return nil
	}
	flags := flag.NewFlagSet("memory search", flag.ContinueOnError)
	task := flags.String("task", "", "search only this session")
	exact := flags.Bool("exact", false, "find the words as written in the logs instead of ranking the turns")
	limit := flags.Int("n", 8, "how many results")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	query := strings.Join(flags.Args(), " ")
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("usage: nodwyn [--repo DIR] memory search [--task SESSION] [--exact] WORDS")
	}
	if *exact {
		hits, err := store.ScanMemory(project, *task, query, *limit)
		if err != nil {
			return err
		}
		for _, h := range hits {
			fmt.Printf("%s  %s  %s seq %d %s\n    %s\n", h.When.Local().Format("2006-01-02 15:04"), h.Task, h.Session, h.Seq, h.Kind, h.Snippet)
			if h.Turn != "" {
				fmt.Printf("    %s\n", h.Turn)
			}
		}
		return nil
	}
	hits, err := store.SearchMemory(project, *task, query, *limit)
	if err != nil {
		return err
	}
	for _, h := range hits {
		level := map[int]string{memory.LevelTurn: "turn", memory.LevelEpisode: "episode"}[h.Level]
		fmt.Printf("%s  %s  %s  %.2f\n    %s\n    %s\n", h.Start.Local().Format("2006-01-02 15:04"), level, h.Task, h.Score, h.Snippet, h.ID)
	}
	return nil
}
