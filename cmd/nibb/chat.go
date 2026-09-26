package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/frontend/plain"
	"github.com/fclaude/nibb/internal/keychain"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
)

// chat is the default command: one session in the line-mode front-end.
func (c *cli) chat() error {
	cfg, err := config.Load(c.paths.ConfigFile)
	if err != nil {
		return err
	}
	if c.model != "" {
		cfg.Control.Model = c.model
	}
	if c.url != "" {
		cfg.Control.URL = c.url
	}
	if err := c.applyBudgets(&cfg); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	// A resumed session keeps the repository it was opened against, unless
	// this command line names one.
	repo := c.repo
	if c.resume != "" && !c.set("repo") {
		repo = ""
	}
	return c.session(cfg, repo)
}

// applyBudgets folds the per-run budget flags into the configuration. Only
// flags actually written are applied, and a hard limit pulls its soft limit
// down with it so the pair stays ordered.
func (c *cli) applyBudgets(cfg *config.Config) error {
	if c.set("max-tool-calls") {
		if c.maxCalls <= 0 {
			return fmt.Errorf("max-tool-calls must be positive")
		}
		cfg.Loop.MaxToolCalls = c.maxCalls
	}
	if c.set("budget-tokens") {
		if c.budgetTokens <= 0 {
			return fmt.Errorf("budget-tokens must be positive")
		}
		cfg.Budget.TokensHard = c.budgetTokens
		cfg.Budget.TokensSoft = min(cfg.Budget.TokensSoft, c.budgetTokens)
	}
	if c.set("budget-actions") {
		if c.budgetActions <= 0 {
			return fmt.Errorf("budget-actions must be positive")
		}
		cfg.Budget.ActionsHard = c.budgetActions
		cfg.Budget.ActionsSoft = min(cfg.Budget.ActionsSoft, c.budgetActions)
	}
	if c.set("budget-wall") {
		hard, err := time.ParseDuration(c.budgetWall)
		if err != nil || hard <= 0 {
			return fmt.Errorf("budget-wall must be a positive duration")
		}
		soft, _ := time.ParseDuration(cfg.Budget.WallSoft)
		cfg.Budget.WallHard = hard.String()
		cfg.Budget.WallSoft = min(soft, hard).String()
	}
	return nil
}

// session opens or resumes a session and runs the line-mode front-end over
// it until the reader quits.
func (c *cli) session(cfg config.Config, repo string) error {
	provider := llm.NewOllama(cfg.Control.URL)
	ctx := context.Background()
	version, err := provider.Ping(ctx)
	if err != nil {
		return fmt.Errorf("control plane %s unreachable: %w", cfg.Control.URL, err)
	}
	fmt.Fprintf(os.Stderr, "control plane: %s %s (ollama %s)\n", cfg.Control.URL, cfg.Control.Model, version)

	var kc *keychain.Store
	var bindings binding.Table
	if !c.noKeychain {
		if kc, bindings, err = openKeychain(c.paths); err != nil {
			return err
		}
	}
	options := core.Options{
		Config: cfg, Paths: c.paths, Repo: repo, Version: versionString(),
		Provider: provider, NoConfirm: c.noConfirm, Keychain: kc, Bindings: bindings,
	}
	var s *core.Session
	if c.resume == "" {
		s, err = core.New(options)
	} else {
		s, err = core.Resume(options, c.resume)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "log: %s\n", log.SessionPath(c.paths.SessionsDir, s.ID()))

	chat := &plain.Chat{
		Session: s, In: os.Stdin, Out: os.Stdout,
		ShowThinking: c.thinking, Interrupt: watchInterrupts(s), ReplayHistory: c.resume != "",
	}
	runErr := chat.Run(ctx)
	if err := s.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "nibb: close:", err)
	}
	return runErr
}

// watchInterrupts turns Ctrl-C into a turn abort while the session is
// working, and into a clean exit when it is not.
func watchInterrupts(s *core.Session) <-chan struct{} {
	interrupt := make(chan struct{}, 1)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		for range signals {
			if s.Busy() {
				select {
				case interrupt <- struct{}{}:
				default:
				}
				continue
			}
			_ = s.Close()
			fmt.Fprintln(os.Stderr)
			os.Exit(130)
		}
	}()
	return interrupt
}
