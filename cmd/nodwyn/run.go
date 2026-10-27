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

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
)

func runTask(args []string) error {
	// "nodwyn run --task ..." and "nodwyn run start --task ..." both start a run.
	command := "start"
	if len(args) > 0 && (args[0] == "start" || args[0] == "status" || args[0] == "stop" || args[0] == "resume") {
		command = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("nodwyn run "+command, flag.ContinueOnError)
	socket := flags.String("socket", "", "daemon socket")
	repo := flags.String("repo", ".", "repository directory")
	task := flags.String("task", "", "UTF-8 task file (at most 64 KiB)")
	wall := flags.String("budget-wall", "", "the whole run's deadline, e.g. 2h (none unless given)")
	turns := flags.Int("max-turns", 0, "the most turns the run may take (none unless given)")
	limits := addLimitFlags(flags, "turn-wall")
	yolo := flags.Bool("yolo", false, "answer this run's ordinary confirms with yes; denials and security confirms are unaffected")
	judge := flags.Bool("judge", false, "the judge answers this run's ordinary confirms instead of a person, allowing a call only when it judges it called for and safe; security confirms are declined; needs a [judge] server whose key is stored")
	unattended := flags.Bool("unattended", false, "nobody answers this run: confirms are declined (or, with --yolo, ordinary ones answered yes), and the run goes on from a turn only when it marked a plan step done or skipped")
	sandbox := flags.String("sandbox", "", `"container" declares this machine the run's disposable sandbox (ADR 55): its tools may read and write any path but nodwyn's own directories and credential files, what its commands start in the background keeps running, and it implies --yolo and --unattended. Only for a machine that is thrown away after the run`)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *sandbox != "" {
		if *sandbox != "container" {
			return errors.New(`--sandbox takes "container": the machine nodwyn runs on is a disposable container`)
		}
		if *judge {
			return errors.New("--sandbox and --judge exclude each other: a sandbox run answers its ordinary confirms itself, as --yolo does")
		}
		*yolo, *unattended = true, true
	}
	var params any
	if command == "start" {
		if flags.NArg() != 0 || *task == "" {
			return errors.New("run requires --task FILE")
		}
		// A run's own limits are optional, as a task's are (ADR 49, amended
		// 2026-10-27).
		if *turns < 0 {
			return errors.New("--max-turns is a number of turns, or 0 for none")
		}
		if *wall != "" {
			if duration, err := time.ParseDuration(*wall); err != nil || duration <= 0 {
				return errors.New("--budget-wall is a positive duration, such as 2h")
			}
		}
		taskLimits, err := limits.over(budget.Task{})
		if err != nil {
			return err
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
		if *judge && *yolo {
			return errors.New("--judge and --yolo exclude each other: the judge answers what --yolo would")
		}
		params = map[string]any{"repo": root, "task": string(data), "auto_approve": *yolo, "unattended": *unattended, "judge": *judge}
		if *wall != "" {
			params.(map[string]any)["wall"] = *wall
		}
		if *turns > 0 {
			params.(map[string]any)["max_turns"] = *turns
		}
		if taskLimits != (budget.Task{}) {
			params.(map[string]any)["limits"] = taskLimits
		}
		if *sandbox != "" {
			params.(map[string]any)["sandbox"] = *sandbox
			fmt.Fprintln(os.Stderr, "sandbox: this machine is the run's disposable sandbox; its tools may change any file but nodwyn's own and credential files, and what it starts in the background keeps running")
		}
		if *yolo {
			fmt.Fprintf(os.Stderr, "yolo: this run's ordinary confirms are answered yes in %s; denials and security confirms still stop it\n", root)
		}
		if *unattended {
			fmt.Fprintln(os.Stderr, "unattended: nobody answers this run; it stops after a turn that marks no plan step")
		}
	} else {
		if flags.NArg() != 1 {
			return errors.New("run " + command + " requires a session ID")
		}
		var invalid bool
		flags.Visit(func(f *flag.Flag) {
			resumeFlag := command == "resume" && (f.Name == "yolo" || f.Name == "unattended" || f.Name == "judge" || f.Name == "sandbox")
			if f.Name != "socket" && !resumeFlag {
				invalid = true
			}
		})
		if invalid {
			return errors.New("run status/stop accepts only --socket and a session ID, and resume also --yolo, --judge, --unattended and --sandbox; limits never reset")
		}
		if command == "resume" {
			// How the run goes on is said again at every resume.
			params = map[string]any{"id": flags.Arg(0), "auto_approve": *yolo, "unattended": *unattended, "judge": *judge}
			if *sandbox != "" {
				params.(map[string]any)["sandbox"] = *sandbox
			}
		} else {
			params = map[string]string{"id": flags.Arg(0)}
		}
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
		return fmt.Errorf("run requires a running nodwyn daemon: %w", err)
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
	if (command == "start" || command == "resume") && *unattended {
		fmt.Fprintln(os.Stderr, "Run submitted. Follow it with nodwyn run status, nodwyn tui --socket", *socket, "or nodwyn ui --socket", *socket+". Disconnecting does not stop the run.")
	} else if command == "start" || command == "resume" {
		fmt.Fprintln(os.Stderr, "Run submitted. Attach with nodwyn tui --socket", *socket, "or nodwyn ui --socket", *socket, "to answer checkpoints. Disconnecting does not stop the run.")
	}
	return nil
}
