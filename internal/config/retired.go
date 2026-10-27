package config

import "github.com/BurntSushi/toml"

// retiredLimits are the keys config.toml set every task's limits with
// before a task's limits became its own (ADR 49, amended 2026-10-27).
var retiredLimits = map[string]bool{
	"budget": true, "budget.tool_calls_soft": true, "budget.tool_calls_hard": true,
	"budget.tokens_soft": true, "budget.tokens_hard": true,
	"budget.actions_soft": true, "budget.actions_hard": true,
	"budget.wall_soft": true, "budget.wall_hard": true,
	"budget.cost_soft": true, "budget.cost_hard": true,
	"loop.max_tool_calls": true,
}

// retiredLimit reports whether k is one of retiredLimits, which a file
// written before may still hold: it loads, and nothing reads it.
func retiredLimit(k toml.Key) bool { return retiredLimits[k.String()] }
