package main

import (
	"fmt"

	"github.com/fclaude/nibb/internal/core"
)

// sessions lists what is on disk. --ids prints only identifiers, which is
// what scripts and the selfcheck consume.
func (c *cli) sessions() error {
	ids := c.fs.Arg(1) == "--ids"
	if c.fs.NArg() > 2 || (c.fs.NArg() == 2 && !ids) {
		return fmt.Errorf("sessions: expected --ids or no arguments")
	}
	infos, err := (core.Store{Dir: c.paths.SessionsDir}).List()
	if err != nil {
		return err
	}
	for _, info := range infos {
		if ids {
			fmt.Println(info.ID)
			continue
		}
		state := "idle"
		if info.InFlight {
			state = "interrupted"
		}
		if info.Truncated {
			state += " (partial tail)"
		}
		fmt.Printf("%s\t%s\t%s\t%q\t%q\n",
			info.ID, info.LastEvent.Format("2006-01-02 15:04:05Z07:00"), state, info.Repo, info.FirstMessage)
	}
	return nil
}
