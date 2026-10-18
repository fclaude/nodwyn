package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/startup"
)

// startup says whether the daemon starts at login, or turns that on or off
// (ADR 53 §11): nodwyn startup, nodwyn startup on, nodwyn startup off.
func (c *cli) startup() error {
	if err := c.only("startup"); err != nil {
		return err
	}
	place, err := startup.Here(runtime.GOOS)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	switch args := c.rest(); {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "on":
		if startup.Temporary(exe) {
			return fmt.Errorf("startup: this nodwyn is a temporary build (go run), gone when it exits: install one and run its nodwyn startup on")
		}
		if err := place.Enable(exe, startup.Environment(os.LookupEnv)); err != nil {
			return fmt.Errorf("startup: %w", err)
		}
	case len(args) == 1 && args[0] == "off":
		if err := place.Disable(); err != nil {
			return fmt.Errorf("startup: %w", err)
		}
	default:
		return fmt.Errorf("startup: use nodwyn startup, nodwyn startup on or nodwyn startup off")
	}
	item := place.Status(exe)
	switch {
	case !item.Supported:
		fmt.Printf("nodwyn has no login item on %s.\n", runtime.GOOS)
	case !item.On:
		fmt.Println("Off: nodwyn's daemon does not start at login, so scheduled runs wait for nodwyn to be open. nodwyn startup on turns it on.")
	case item.Other:
		fmt.Printf("On, for another nodwyn: %s starts it at login. nodwyn startup on makes it start this one (%s).\n", item.Path, exe)
	default:
		fmt.Printf("On: %s starts nodwyn's daemon at your next login, and scheduled runs go on with no window open. nodwyn startup off turns it off.\n", item.Path)
	}
	return nil
}

// daemonOptions are what a daemon on this computer has and a test's does
// not: the desktop it shows notices on, and its login item.
func daemonOptions() []rpc.ServeOption {
	options := []rpc.ServeOption{rpc.WithNotify(desktopNotify)}
	place, err := startup.Here(runtime.GOOS)
	exe, exeErr := os.Executable()
	if err == nil && exeErr == nil {
		options = append(options, rpc.WithStartup(place, exe))
	}
	return options
}

// desktopNotify shows one of the daemon's fixed notices on this computer's
// desktop: notify-send on Linux, osascript on macOS.
func desktopNotify(title, body string) error {
	switch runtime.GOOS {
	case "linux":
		return exec.Command("notify-send", "--app-name=nodwyn", title, body).Run()
	case "darwin":
		// The words are the script's arguments, never part of the script.
		return exec.Command("osascript", "-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body).Run()
	}
	return fmt.Errorf("no desktop notifications on %s", runtime.GOOS)
}
