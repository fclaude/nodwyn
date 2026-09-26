package config

import (
	"fmt"
	"time"

	"github.com/fclaude/nibb/internal/budget"
)

// Budget uses explicit TOML names; wall durations stay human-readable on disk.
type Budget struct {
	ToolCallsSoft int64  `toml:"tool_calls_soft"`
	ToolCallsHard int64  `toml:"tool_calls_hard"`
	TokensSoft    int64  `toml:"tokens_soft"`
	TokensHard    int64  `toml:"tokens_hard"`
	ActionsSoft   int64  `toml:"actions_soft"`
	ActionsHard   int64  `toml:"actions_hard"`
	WallSoft      string `toml:"wall_soft"`
	WallHard      string `toml:"wall_hard"`
}

// Limits validates the configured budget and converts it for budget.Start.
// legacyCalls is loop.max_tool_calls (or --max-tool-calls); when set it
// becomes the hard tool-call limit.
func (b Budget) Limits(legacyCalls int) (budget.Limits, error) {
	wallSoft, e1 := time.ParseDuration(b.WallSoft)
	wallHard, e2 := time.ParseDuration(b.WallHard)
	if e1 != nil || e2 != nil {
		return budget.Limits{}, fmt.Errorf("budget wall limits must be durations")
	}
	limits := budget.Limits{
		ToolCalls: budget.Limit{Soft: b.ToolCallsSoft, Hard: b.ToolCallsHard},
		Tokens:    budget.Limit{Soft: b.TokensSoft, Hard: b.TokensHard},
		Actions:   budget.Limit{Soft: b.ActionsSoft, Hard: b.ActionsHard},
		Wall:      budget.Limit{Soft: int64(wallSoft), Hard: int64(wallHard)},
	}
	for _, entry := range []struct {
		name  string
		limit budget.Limit
	}{{"tool_calls", limits.ToolCalls}, {"tokens", limits.Tokens}, {"actions", limits.Actions}, {"wall", limits.Wall}} {
		if entry.limit.Soft <= 0 || entry.limit.Hard <= 0 || entry.limit.Soft > entry.limit.Hard {
			return budget.Limits{}, fmt.Errorf("budget.%s requires 0 < soft <= hard", entry.name)
		}
	}
	if legacyCalls < 0 {
		return budget.Limits{}, fmt.Errorf("loop.max_tool_calls must be positive when set")
	}
	if legacyCalls > 0 {
		limits.ToolCalls.Hard = int64(legacyCalls)
		limits.ToolCalls.Soft = min(limits.ToolCalls.Soft, limits.ToolCalls.Hard)
	}
	return limits, nil
}
