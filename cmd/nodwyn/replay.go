package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fclaude/nodwyn/internal/frontend/plain"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
)

// replay prints a session log as a transcript, or with --window SEQ
// reconstructs the model request recorded at that sequence. A log with a
// truncated tail still replays: its valid prefix is shown and the damage is
// reported afterwards.
func (c *cli) replay() error {
	if err := c.only("replay", "show-thinking"); err != nil {
		return err
	}
	if c.fs.NArg() < 2 {
		return fmt.Errorf("replay: session id or path required")
	}
	events, readErr := log.ReadFile(sessionPath(c.paths.SessionsDir, c.fs.Arg(1)))
	if readErr != nil && len(events) == 0 {
		return readErr
	}
	if c.fs.NArg() > 2 {
		if c.fs.NArg() != 4 || c.fs.Arg(2) != "--window" {
			return fmt.Errorf("usage: nodwyn replay SESSION [--window SEQ]")
		}
		// A reconstruction of a damaged log would not be the request that
		// was sent, so the read error is fatal here even though the
		// transcript tolerates it.
		if readErr != nil {
			return readErr
		}
		return replayWindow(events, c.fs.Arg(3))
	}
	plain.Replay(events, os.Stdout, c.thinking)
	if readErr != nil {
		fmt.Fprintln(os.Stderr, "nodwyn: note:", readErr)
	}
	return nil
}

func replayWindow(events []log.Event, arg string) error {
	seq, err := strconv.ParseUint(arg, 10, 64)
	if err != nil || seq == 0 {
		return fmt.Errorf("replay: --window requires a positive event sequence")
	}
	request, err := window.ReplayRequest(events, seq)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(request)
}

// sessionPath accepts either a session id or a path to a log file.
func sessionPath(dir, arg string) string {
	if strings.Contains(arg, string(filepath.Separator)) || strings.HasSuffix(arg, ".jsonl") {
		return arg
	}
	return log.SessionPath(dir, arg)
}
