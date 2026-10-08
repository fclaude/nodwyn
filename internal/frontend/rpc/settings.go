package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/policyfile"
)

// settingKind is the JSON a setting takes.
type settingKind int

const (
	text settingKind = iota
	whole
	number
	boolean
)

// settable is what Settings may change in config.toml (settings.write), by
// dotted name, and the kind of value each takes; config.Parse judges the
// value. Settings changes only on the computer nodwyn runs on (ADR 30), so
// what a task asks a person about, [approval] (ADR 35), is here. What
// decides where content goes is not: the model's address and key
// (settings.control.write), a provider's address and key
// (settings.provider.write), credentials and bindings, the egress proxy and
// the rest of [policy] are set where they are reviewed, or in config.toml by
// hand. judge.server only chooses which of those providers the judge asks
// (ADR 46), as choosing a default model chooses among them. A model's own settings (its context length,
// prices, whether it takes images) are not here either: each model has its
// own, set with settings.model.write (ADR 41, amended 2026-10-05).
// The one transport exception here is an explicit local user choice (ADR 45).
var settable = map[string]settingKind{
	"policy.allow_insecure_http": boolean,
	"approval.writes":            text, "approval.commands": text, "approval.dont_ask": boolean,

	"budget.tool_calls_soft": whole, "budget.tool_calls_hard": whole,
	"budget.tokens_soft": whole, "budget.tokens_hard": whole,
	"budget.actions_soft": whole, "budget.actions_hard": whole,
	"budget.wall_soft": text, "budget.wall_hard": text,
	"budget.cost_soft": number, "budget.cost_hard": number,
	"loop.request_timeout": text, "loop.shell_timeout": text,

	"window.high_water": whole, "window.low_water": whole,
	"window.max_result_tokens": whole, "window.output_reserve": whole,

	"judge.server": text, "judge.content": text, "judge.approve_threshold": number, "judge.timeout": text, "judge.model": text,
	"judge.price_input": number, "judge.price_output": number,

	"mcp.runtime": text,
}

// decode reads a setting's new value: nil (null) to remove it, else a
// string, int64, float64 or bool, as config.Change takes them.
func (k settingKind) decode(raw json.RawMessage) (any, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	switch k {
	case whole:
		var n int64
		if json.Unmarshal(raw, &n) != nil {
			return nil, fmt.Errorf("takes a whole number")
		}
		return n, nil
	case number:
		var f float64
		if json.Unmarshal(raw, &f) != nil {
			return nil, fmt.Errorf("takes a number")
		}
		return f, nil
	case boolean:
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return nil, fmt.Errorf("takes true or false")
		}
		return b, nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil, fmt.Errorf("takes a string")
	}
	if s == "" {
		return nil, fmt.Errorf("is empty; send null for its default")
	}
	return s, nil
}

// settingValue is one setting as settings.read reports it.
type settingValue struct {
	// Value is what tasks opened now get; null for an optional setting left
	// unset (the server decides, or the limit is off).
	Value   any  `json:"value"`
	Default any  `json:"default"`
	Set     bool `json:"set"` // config.toml sets it
}

type settingsWriteParams struct {
	// Set maps a setting's dotted name to its new value; null removes it
	// from config.toml, so its default applies.
	Set map[string]json.RawMessage `json:"set"`
}

// values reports every settable setting as the daemon holds it for new
// tasks, with its default and whether config.toml sets it.
func (s *server) values() map[string]settingValue {
	set := map[string]bool{}
	if raw, err := os.ReadFile(s.options.Paths.ConfigFile); err == nil {
		set, _ = config.Keys(raw)
	}
	defaults := config.Default()
	out := make(map[string]settingValue, len(settable))
	for name := range settable {
		table, key := splitSetting(name)
		value, _ := s.options.Config.Value(table, key)
		def, _ := defaults.Value(table, key)
		out[name] = settingValue{Value: value, Default: def, Set: set[name]}
	}
	return out
}

func splitSetting(name string) (table, key string) {
	i := strings.LastIndexByte(name, '.')
	return name[:i], name[i+1:]
}

// settingsWrite answers settings.write: it changes the named settings in
// config.toml together, keeping its other lines, under the lock sessions
// take to save a setting, and applies them to tasks opened afterwards.
// Tasks already open keep what they started with, except "Don't ask": a
// person who takes the switch away takes it from every open task too, so
// none goes on approving everything after Settings says nothing may.
func (s *server) settingsWrite(req request, client *peer) (any, *rpcError) {
	var p settingsWriteParams
	if err := params(req.Params, &p, "set"); err != nil {
		return nil, err
	}
	if len(p.Set) == 0 {
		return nil, failure(-32602, "set must name at least one setting")
	}
	var changes []config.Change
	tables := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(p.Set)) {
		kind, ok := settable[name]
		if !ok {
			return nil, failure(-32602, fmt.Sprintf("%s is not a setting Settings can change", name))
		}
		value, err := kind.decode(p.Set[name])
		if err != nil {
			return nil, failure(-32602, fmt.Sprintf("%s %v", name, err))
		}
		table, key := splitSetting(name)
		changes = append(changes, config.Change{Table: table, Key: key, Value: value})
		tables[table] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	next := s.options.Config
	err := policyfile.Update(ctx, s.options.Paths.ConfigFile, func(raw []byte) ([]byte, error) {
		out, parsed, err := config.Edit(raw, changes...)
		if err != nil {
			return nil, err
		}
		// The daemon takes the changed tables from the file; the rest of
		// what it holds must still agree with them.
		next = withTables(s.options.Config, parsed, tables)
		if err := next.Validate(); err != nil {
			return nil, err
		}
		// A ChatGPT plan's sign-in is never sent to the Decisions API (the
		// keychain refuses), so a judge on one would never answer (ADR 47).
		if j := next.Judge.Server; tables["judge"] && j != "" && core.PlanSignIn(s.options.Keychain, next.Servers[j].Credential) {
			return nil, fmt.Errorf("judge.server: %s signs in with a ChatGPT plan, which only reaches its chat models; the judge asks OpenAI's Decisions API with an API key, so add an OpenAI provider with one", j)
		}
		return out, nil
	})
	if err != nil {
		return nil, failure(-32602, err.Error())
	}
	if err := os.Chmod(s.options.Paths.ConfigFile, 0o600); err != nil {
		return nil, failure(-32000, err.Error())
	}
	s.options.Config = next
	if !next.Approval.DontAsk {
		for _, open := range s.sessions {
			if on, _ := open.session.ApproveAll(); on {
				if err := open.session.SetApproveAll(false, client.actor); err != nil {
					return nil, failure(-32000, err.Error())
				}
			}
		}
	}
	return s.settings(), nil
}

// withTables is held with the tables named in tables taken from parsed.
func withTables(held, parsed config.Config, tables map[string]bool) config.Config {
	for table := range tables {
		switch table {
		case "policy":
			held.Policy = parsed.Policy
		case "approval":
			held.Approval = parsed.Approval
		case "budget":
			held.Budget = parsed.Budget
		case "loop":
			held.Loop = parsed.Loop
		case "window":
			held.Window = parsed.Window
		case "judge":
			held.Judge = parsed.Judge
		case "mcp":
			held.MCP = parsed.MCP
		default:
			panic("rpc: settable table without a copy: " + table)
		}
	}
	return held
}
