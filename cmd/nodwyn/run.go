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
	wall := flags.String("budget-wall", "", "required total run deadline, e.g. 2h")
	turns := flags.Int("max-turns", 0, "required maximum number of turns")
	cost := flags.Float64("budget-cost", 0, "spend limit for the whole run in US dollars, as far as costs are known (replaces budget.cost_hard)")
	yolo := flags.Bool("yolo", false, "answer this run's ordinary confirms with yes; denials and security confirms are unaffected")
	judge := flags.Bool("judge", false, "the judge answers this run's ordinary confirms instead of a person, allowing a call only when it judges it called for and safe; security confirms are declined; needs a [judge] server whose key is stored")
	unattended := flags.Bool("unattended", false, "nobody answers this run: confirms are declined (or, with --yolo, ordinary ones answered yes), soft budget checkpoints go on to the hard limit, and the run goes on from a turn only when it marked a plan step done")
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
		if *judge && *yolo {
			return errors.New("--judge and --yolo exclude each other: the judge answers what --yolo would")
		}
		if *cost < 0 {
			return errors.New("--budget-cost is US dollars, more than 0")
		}
		params = map[string]any{"repo": root, "task": string(data), "wall": *wall, "max_turns": *turns, "auto_approve": *yolo, "unattended": *unattended, "judge": *judge}
		if *cost > 0 {
			params.(map[string]any)["cost"] = *cost
		}
		if *yolo {
			fmt.Fprintf(os.Stderr, "yolo: this run's ordinary confirms are answered yes in %s; denials and security confirms still stop it\n", root)
		}
		if *unattended {
			fmt.Fprintln(os.Stderr, "unattended: nobody answers this run; it stops after a turn that marks no plan step done")
		}
	} else {
		if flags.NArg() != 1 {
			return errors.New("run " + command + " requires a session ID")
		}
		var invalid bool
		flags.Visit(func(f *flag.Flag) {
			resumeFlag := command == "resume" && (f.Name == "yolo" || f.Name == "unattended" || f.Name == "judge")
			if f.Name != "socket" && !resumeFlag {
				invalid = true
			}
		})
		if invalid {
			return errors.New("run status/stop accepts only --socket and a session ID, and resume also --yolo, --judge and --unattended; limits never reset")
		}
		if command == "resume" {
			// How the run goes on is said again at every resume.
			params = map[string]any{"id": flags.Arg(0), "auto_approve": *yolo, "unattended": *unattended, "judge": *judge}
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
