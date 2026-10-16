package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/log"
)

func turnOutcome(ctx context.Context, err error) log.TurnEnd {
	if ctx.Err() != nil {
		err = context.Cause(ctx)
	}
	result := log.TurnEnd{Text: "turn ended", Outcome: "completed"}
	if err == nil {
		return result
	}
	result.Error = err.Error()
	var stop *budget.Stop
	switch {
	case errors.As(err, &stop):
		result.Outcome = "stopped"
		switch {
		case stop.Spend:
			// The task's limit, not the turn's: no later turn may start.
			result.Outcome = "spend_limit"
		case stop.Hard:
			result.Outcome = "hard_budget"
		}
	case errors.Is(err, context.Canceled):
		result.Outcome = "canceled"
	default:
		result.Outcome = "error"
	}
	return result
}

// whereStopped says, in one line, where a turn that stopped short had got
// to: how far through its plan, which files the file tools wrote, and the
// last command it ran with how it ended. A turn that hit a budget in the
// middle of go test, was stopped for polling, or met a 402 ended with an
// error line and nothing about the work, so the person had to read the
// transcript to learn whether it was safe to go on
// (20261003T002506Z-f564a28c, 20261004T182412Z-747045aa; ADR 18, amended
// 2026-10-05). It is read from the log, redacted already, and runs nothing:
// git is not asked, since which program a repository's git configuration
// would run is not the host's to leave to a stranger's clone. first is the
// number of events the session held when the turn began: only this turn's
// events count, whatever kind of work it was.
func (s *Session) whereStopped(first int) string {
	events := s.EventsSoFar()[first:]
	var parts []string

	s.turnMu.Lock()
	plan, marked := s.turnPlan, len(s.turnMarks)
	next := 0
	steps := log.GatePlan{Plan: plan}.Steps()
	for i := range steps {
		if _, ok := s.turnMarks[i+1]; !ok {
			next = i + 1
			break
		}
	}
	s.turnMu.Unlock()
	if len(steps) > 0 {
		said := fmt.Sprintf("plan: %d of %d steps marked", marked, len(steps))
		if next > 0 {
			said += fmt.Sprintf(", next is step %d (%s)", next, shorten(steps[next-1], 90))
		}
		parts = append(parts, said)
	}

	var written, command []string // written paths in order; the last command's line
	ids := map[string]string{}    // call id to the path it writes or the command it runs
	var status string
	for _, ev := range events {
		switch ev.Kind {
		case log.KindToolCall:
			var c log.ToolCallEvent
			if ev.Decode(&c) != nil {
				continue
			}
			var args struct {
				Path string   `json:"path"`
				Cmd  string   `json:"cmd"`
				Argv []string `json:"args"`
			}
			if json.Unmarshal(c.Args, &args) != nil {
				continue
			}
			switch c.Name {
			case "fs-write", "fs-edit":
				ids[c.ID] = args.Path
			case "shell-exec":
				ids[c.ID] = "\x00" + shorten(strings.Join(append([]string{args.Cmd}, args.Argv...), " "), 120)
			}
		case log.KindToolResult:
			var r log.ToolResult
			if ev.Decode(&r) != nil {
				continue
			}
			what, ok := ids[r.ID]
			if !ok {
				continue
			}
			if strings.HasPrefix(what, "\x00") {
				command = []string{strings.TrimPrefix(what, "\x00")}
				status = "did not run"
				if r.OK {
					var out struct {
						Status int `json:"status"`
					}
					if json.Unmarshal([]byte(r.Value), &out) == nil {
						status = fmt.Sprintf("exited %d", out.Status)
					}
				}
			} else if r.OK && what != "" && !slices.Contains(written, what) {
				written = append(written, what)
			}
		}
	}
	if len(written) > 0 {
		list := strings.Join(written[:min(len(written), 5)], ", ")
		if len(written) > 5 {
			list += fmt.Sprintf(" and %d more", len(written)-5)
		}
		parts = append(parts, "files written this turn: "+list)
	}
	if len(command) > 0 {
		parts = append(parts, fmt.Sprintf("last command: %s (%s)", command[0], status))
	}
	if len(parts) == 0 {
		return "no file written, no command run and no plan step marked in this turn"
	}
	return strings.Join(parts, "; ")
}
