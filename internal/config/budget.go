package config

import (
	"fmt"
	"math"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
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
	// A spend limit, in US dollars, on what a task's model requests cost as
	// far as known (priced by the provider or by the model's price_*). Off
	// unless cost_hard is set. It counts the whole task, every turn of a
	// run included: past cost_soft a checkpoint asks; at cost_hard the turn
	// stops and no later turn starts. cost_soft unset is cost_hard.
	CostSoft float64 `toml:"cost_soft"`
	CostHard float64 `toml:"cost_hard"`
}

// WithCostHard returns b with a spend limit of usd, for one run or session:
// a soft limit above it is pulled down to it. 0 leaves b as it is.
func (b Budget) WithCostHard(usd float64) Budget {
	if usd > 0 {
		b.CostHard = usd
		b.CostSoft = min(b.CostSoft, usd)
	}
	return b
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
	switch soft, hard := b.CostSoft, b.CostHard; {
	case math.IsNaN(soft) || math.IsNaN(hard) || math.IsInf(soft, 0) || math.IsInf(hard, 0) || soft < 0 || hard < 0:
		return budget.Limits{}, fmt.Errorf("budget.cost_soft and cost_hard are US dollars, at least 0")
	case hard == 0 && soft > 0:
		return budget.Limits{}, fmt.Errorf("budget.cost_soft needs budget.cost_hard: the spend limit is off without it")
	case soft > hard:
		return budget.Limits{}, fmt.Errorf("budget.cost requires soft <= hard")
	case hard > 0:
		if soft == 0 {
			soft = hard
		}
		limits.Cost = budget.Limit{Soft: budget.Micros(soft), Hard: budget.Micros(hard)}
		if limits.Cost.Hard <= 0 || limits.Cost.Soft <= 0 {
			return budget.Limits{}, fmt.Errorf("budget.cost_hard is below a millionth of a dollar")
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
