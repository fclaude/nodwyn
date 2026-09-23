package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fclaude/nibb/internal/frontend/rpc"
)

func runTask(args []string) error {
	command := "start"
	if len(args) > 0 && (args[0] == "status" || args[0] == "stop" || args[0] == "resume") {
		command = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("nibb run "+command, flag.ContinueOnError)
	socket := flags.String("socket", "", "daemon socket")
	repo := flags.String("repo", ".", "repository directory")
	task := flags.String("task", "", "UTF-8 task file (at most 64 KiB)")
	wall := flags.String("budget-wall", "", "required total run deadline, e.g. 2h")
	turns := flags.Int("max-turns", 0, "required maximum number of turns")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var params any
	if command == "start" {
		if flags.NArg() != 0 || *task == "" || *turns < 1 {
			return errors.New("run requires --task FILE --budget-wall DURATION --max-turns N")
		}
		duration, err := time.ParseDuration(*wall)
		if err != nil || duration <= 0 {
			return errors.New("run requires a positive --budget-wall")
		}
		file, err := os.Open(*task)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		file.Close()
		if err != nil {
			return err
		}
		if len(data) > 64<<10 {
			return errors.New("run task exceeds 64 KiB")
		}
		root, err := filepath.Abs(*repo)
		if err != nil {
			return err
		}
		params = map[string]any{"repo": root, "task": string(data), "wall": *wall, "max_turns": *turns}
	} else {
		if flags.NArg() != 1 {
			return errors.New("run " + command + " requires a session ID")
		}
		var invalid bool
		flags.Visit(func(f *flag.Flag) {
			if f.Name != "socket" {
				invalid = true
			}
		})
		if invalid {
			return errors.New("run status/stop/resume accepts only --socket and session ID; limits never reset")
		}
		params = map[string]string{"id": flags.Arg(0)}
	}
	if *socket == "" {
		path, err := rpc.DefaultSocket()
		if err != nil {
			return err
		}
		*socket = path
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := rpc.Dial(ctx, *socket)
	if err != nil {
		return fmt.Errorf("run requires a running nibb daemon: %w", err)
	}
	defer client.Close()
	method := command
	if method == "status" {
		method = "info"
	}
	var result json.RawMessage
	if err := client.Call(ctx, "run."+method, params, &result); err != nil {
		return err
	}
	fmt.Println(string(result))
	if command == "start" || command == "resume" {
		fmt.Fprintln(os.Stderr, "Run submitted. Attach with nibb tui --socket", *socket, "or nibb ui --socket", *socket, "to answer checkpoints. Disconnecting does not stop the run.")
	}
	return nil
}
