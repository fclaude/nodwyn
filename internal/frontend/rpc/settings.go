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
	"github.com/fclaude/nodwyn/internal/policyfile"
)

// settingKind is the JSON a setting takes.
type settingKind int

const (
	text settingKind = iota
	whole
	number
	flag
)

// settable is what Settings may change in config.toml (settings.write), by
// dotted name, and the kind of value each takes; config.Parse judges the
// value. Settings changes only on the computer nodwyn runs on (ADR 30), so
// what a task asks a person about, [approval] (ADR 35), is here. What
// decides where content goes is not: the model's address and key
// (settings.control.write), the judge's address, credentials and bindings,
// the egress proxy and the [policy] table are set where they are reviewed,
// or in config.toml by hand.
var settable = map[string]settingKind{
	"approval.writes": text, "approval.commands": text,

	"budget.tool_calls_soft": whole, "budget.tool_calls_hard": whole,
	"budget.tokens_soft": whole, "budget.tokens_hard": whole,
	"budget.actions_soft": whole, "budget.actions_hard": whole,
	"budget.wall_soft": text, "budget.wall_hard": text,
	"budget.cost_soft": number, "budget.cost_hard": number,
	"loop.request_timeout": text, "loop.shell_timeout": text,

	"window.high_water": whole, "window.low_water": whole,
	"window.max_result_tokens": whole, "window.output_reserve": whole,

	"control.num_ctx": whole, "control.max_output": whole, "control.output_field": text, "control.vision": flag,
	"control.keep_alive": text, "control.temperature": number, "control.seed": whole, "control.cache_key": text,
	"control.price_input": number, "control.price_cached_input": number, "control.price_output": number,

	"jev.content": text, "jev.approve_threshold": number, "jev.timeout": text, "jev.model": text,
	"jev.price_input": number, "jev.price_output": number,

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
	case flag:
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
// Tasks already open keep what they started with.
func (s *server) settingsWrite(req request, _ *peer) (any, *rpcError) {
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
		return out, next.Validate()
	})
	if err != nil {
		return nil, failure(-32602, err.Error())
	}
	if err := os.Chmod(s.options.Paths.ConfigFile, 0o600); err != nil {
		return nil, failure(-32000, err.Error())
	}
	s.options.Config = next
	if tables["control"] {
		forgetProbes()
	}
	return s.settings(), nil
}

// withTables is held with the tables named in tables taken from parsed.
func withTables(held, parsed config.Config, tables map[string]bool) config.Config {
	for table := range tables {
		switch table {
		case "approval":
			held.Approval = parsed.Approval
		case "budget":
			held.Budget = parsed.Budget
		case "loop":
			held.Loop = parsed.Loop
		case "window":
			held.Window = parsed.Window
		case "control":
			held.Control = parsed.Control
		case "jev":
			held.Jev = parsed.Jev
		case "mcp":
			held.MCP = parsed.MCP
		default:
			panic("rpc: settable table without a copy: " + table)
		}
	}
	return held
}
