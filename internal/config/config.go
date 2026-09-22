// Package config locates nibb's files and loads its configuration.
//
// Layout (XDG, also on macOS — nibb is a developer tool):
//
//	$XDG_CONFIG_HOME/nibb/config.toml     control plane, window budget, policy
//	$XDG_CONFIG_HOME/nibb/bindings.toml   the user-owned binding table (P3)
//	$XDG_CONFIG_HOME/nibb/keychain        encrypted credential store (P3) — never in the log
//	$XDG_DATA_HOME/nibb/sessions/         one .jsonl per session (the log)
//
// Missing files mean defaults; a present file is parsed over the defaults and
// validated. There is no secret in any of the files this package reads.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/fclaude/nibb/internal/detect/l1"
)

// Paths are the directories and files nibb uses.
type Paths struct {
	ConfigDir   string
	DataDir     string
	SessionsDir string
	ConfigFile  string
	Bindings    string
	Keychain    string
}

// DefaultPaths resolves the XDG layout for the current user.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("config: home dir: %w", err)
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	return PathsUnder(filepath.Join(cfg, "nibb"), filepath.Join(data, "nibb")), nil
}

// PathsUnder builds Paths from explicit config and data directories.
func PathsUnder(configDir, dataDir string) Paths {
	return Paths{
		ConfigDir:   configDir,
		DataDir:     dataDir,
		SessionsDir: filepath.Join(dataDir, "sessions"),
		ConfigFile:  filepath.Join(configDir, "config.toml"),
		Bindings:    filepath.Join(configDir, "bindings.toml"),
		Keychain:    filepath.Join(configDir, "keychain"),
	}
}

// Config is config.toml.
type Config struct {
	Control Control `toml:"control"`
	Window  Window  `toml:"window"`
	Policy  Policy  `toml:"policy"`
	Loop    Loop    `toml:"loop"`
	Budget  Budget  `toml:"budget"`
	Jev     Jev     `toml:"jev"`
}

// Jev is the judge's configuration (ADR 19/20). Jev is keyed: it runs only
// when a credential of class jev-key is registered and bound to the URL's
// host. Content is the level of what may leave (ADR 19); in v0 the gate
// sends the ask and the plan whatever the level says, so "plan" is the
// honest default and the higher levels are reserved.
type Jev struct {
	URL     string `toml:"url"`     // base URL; default https://api.typesafe.ai
	Model   string `toml:"model"`   // default jev-latest
	Timeout string `toml:"timeout"` // per request, e.g. "10s"
	Content string `toml:"content"` // "none" | "plan" | "plan+summaries" | "full" (default "plan")
}

// Valid content levels, lowest to highest.
const (
	JevContentNone    = "none"
	JevContentPlan    = "plan"
	JevContentPlanSum = "plan+summaries"
	JevContentFull    = "full"
)

// Enabled reports whether Jev may be used at all: a URL and a content level
// other than "none". Whether it actually runs also needs a jev-key credential.
func (j Jev) Enabled() bool {
	return j.URL != "" && j.Content != JevContentNone
}

// JevContentRank orders levels so "cap" comparisons are well-defined.
func JevContentRank(level string) int {
	switch level {
	case JevContentNone:
		return 0
	case JevContentPlan:
		return 1
	case JevContentPlanSum:
		return 2
	case JevContentFull:
		return 3
	default:
		return 3
	}
}

// Loop bounds one turn of the agent loop.
type Loop struct {
	MaxToolCalls   int    `toml:"max_tool_calls"`  // legacy hard override; zero when absent, --max-tool-calls overrides
	RequestTimeout string `toml:"request_timeout"` // per model request, e.g. "30m"
	ShellTimeout   string `toml:"shell_timeout"`   // per shell-exec, e.g. "10m"
}

// Control describes the control-plane model (ADR 8). It is configured, not
// keyed: the workshop endpoint is reachable only inside the tailnet.
type Control struct {
	Provider  string `toml:"provider"`   // "ollama" (bootstrap); others behind the same interface later
	URL       string `toml:"url"`        // base URL, no trailing path
	Model     string `toml:"model"`      // model id as the provider names it
	NumCtx    int    `toml:"num_ctx"`    // backend context length to request
	KeepAlive string `toml:"keep_alive"` // provider keep-alive, e.g. "30m"
}

// Window is the context policy (ADR 9, amended): hysteresis instead of a
// single budget. Above HighWater the window is trimmed (oldest first, tool
// results before assistant text, user messages never) down to LowWater,
// then left alone until it crosses HighWater again, so the request prefix
// stays byte-identical between trims and the model server's cache hits.
type Window struct {
	HighWater       int `toml:"high_water"`        // trim when the estimate exceeds this
	LowWater        int `toml:"low_water"`         // trim down to this
	MaxResultTokens int `toml:"max_result_tokens"` // a tool result larger than this is shown head+tail
	OutputReserve   int `toml:"output_reserve"`    // tokens reserved for generation
}

// Policy holds the guard's user-settable defaults (ADR 6).
type Policy struct {
	Unbound       string   `toml:"unbound"`        // "deny" (default) or "confirm" for unbound secret-bearing egress
	AllowCommands []string `toml:"allow_commands"` // standing approvals: argv prefixes that run without a confirm
	AllowWrites   []string `toml:"allow_writes"`   // standing approvals: repository-relative path prefixes writable without a confirm

	// L1 is the secret-scanner policy (Phase 4).
	L1 L1 `toml:"l1"`
}

// L1 configures the L1 secret scanner (Phase 4).
type L1 struct {
	Enabled bool `toml:"enabled"` // default true
	// AllowClasses suppresses whole classes (e.g. "jwt" in a repo full of
	// test tokens). Validated against the scanner's class names.
	AllowClasses []string `toml:"allow_classes"`
	// AllowValues suppresses exact strings: documented placeholders such as
	// AWS's AKIAIOSFODNN7EXAMPLE. Never put a real secret here — this file
	// is plaintext (plan §3.3).
	AllowValues []string `toml:"allow_values"`
}

// RepoAllow is <repo>/.nibb/allow.toml: standing approvals for one
// repository, merged with policy.allow_commands.
type RepoAllow struct {
	Commands []string `toml:"commands"`
	Writes   []string `toml:"writes"`
}

// LoadRepoAllow reads a repository's allow file. Missing means none.
func LoadRepoAllow(path string) (RepoAllow, error) {
	var r RepoAllow
	md, err := toml.DecodeFile(path, &r)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RepoAllow{}, nil
		}
		return RepoAllow{}, fmt.Errorf("allow: %s: %w", path, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return RepoAllow{}, fmt.Errorf("allow: %s: unknown keys: %v", path, undec)
	}
	return r, nil
}

// Default is the configuration nibb runs with when no config.toml exists.
func Default() Config {
	return Config{
		Control: Control{
			Provider:  "ollama",
			URL:       "http://workshop.example:11434",
			Model:     "qwen3.8-27b-96k:latest",
			NumCtx:    98304,
			KeepAlive: "30m",
		},
		Window: Window{HighWater: 60 * 1024, LowWater: 30 * 1024, MaxResultTokens: 3 * 1024, OutputReserve: 8 * 1024},
		Policy: Policy{Unbound: "deny", L1: L1{Enabled: true}},
		Loop:   Loop{MaxToolCalls: 0, RequestTimeout: "30m", ShellTimeout: "10m"},
		Budget: Budget{ToolCallsSoft: 50, ToolCallsHard: 500, TokensSoft: 1_000_000, TokensHard: 10_000_000, ActionsSoft: 25, ActionsHard: 200, WallSoft: "20m", WallHard: "4h"},
		// Jev defaults: no service (nibb runs file-only, gate is "plan"), full
		// content when one is configured. A developer points jev.url at their
		// instance to enable the gate (ADR 19).
		Jev: Jev{URL: "https://api.typesafe.ai", Model: "jev-latest", Timeout: "10s", Content: JevContentPlan},
	}
}

// Load reads path over Default. A missing file is not an error.
func Load(path string) (Config, error) {
	c := Default()
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return Config{}, fmt.Errorf("config: %s: %w", path, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return Config{}, fmt.Errorf("config: %s: unknown keys: %v", path, undec)
	}
	if md.IsDefined("loop", "max_tool_calls") && c.Loop.MaxToolCalls <= 0 {
		return Config{}, fmt.Errorf("loop.max_tool_calls must be positive when set")
	}
	if md.IsDefined("loop", "max_tool_calls") && md.IsDefined("budget", "tool_calls_hard") {
		return Config{}, fmt.Errorf("set budget.tool_calls_hard or legacy loop.max_tool_calls, not both")
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", path, err)
	}
	return c, nil
}

// Validate rejects configurations the harness must not run with.
func (c Config) Validate() error {
	switch c.Control.Provider {
	case "ollama":
	default:
		return fmt.Errorf("control.provider %q is not supported", c.Control.Provider)
	}
	if c.Control.URL == "" || c.Control.Model == "" {
		return errors.New("control.url and control.model are required")
	}
	if c.Control.NumCtx <= 0 {
		return errors.New("control.num_ctx must be positive")
	}
	if c.Window.HighWater <= 0 || c.Window.LowWater <= 0 || c.Window.OutputReserve <= 0 || c.Window.MaxResultTokens <= 0 {
		return errors.New("window values must be positive")
	}
	if c.Window.LowWater >= c.Window.HighWater {
		return fmt.Errorf("window.low_water (%d) must be below window.high_water (%d)", c.Window.LowWater, c.Window.HighWater)
	}
	if c.Window.HighWater+c.Window.OutputReserve > c.Control.NumCtx {
		return fmt.Errorf("window.high_water + window.output_reserve (%d) exceeds control.num_ctx (%d)",
			c.Window.HighWater+c.Window.OutputReserve, c.Control.NumCtx)
	}
	switch c.Policy.Unbound {
	case "deny", "confirm":
	default:
		return fmt.Errorf("policy.unbound must be \"deny\" or \"confirm\", got %q", c.Policy.Unbound)
	}
	for _, cl := range c.Policy.L1.AllowClasses {
		if !l1.IsClass(cl) {
			return fmt.Errorf("policy.l1.allow_classes: unknown class %q (known: %v)", cl, l1.ClassNames())
		}
	}
	if _, err := c.Budget.Limits(c.Loop.MaxToolCalls); err != nil {
		return err
	}
	for name, v := range map[string]string{"loop.request_timeout": c.Loop.RequestTimeout, "loop.shell_timeout": c.Loop.ShellTimeout} {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			return fmt.Errorf("%s must be a positive duration, got %q", name, v)
		}
	}
	if c.Jev.URL != "" {
		u, err := url.Parse(c.Jev.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("jev.url must be an absolute URL, got %q", c.Jev.URL)
		}
	}
	if d, err := time.ParseDuration(c.Jev.Timeout); err != nil || d <= 0 {
		return fmt.Errorf("jev.timeout must be a positive duration, got %q", c.Jev.Timeout)
	}
	if c.Jev.Model == "" {
		return errors.New("jev.model is required")
	}
	switch c.Jev.Content {
	case JevContentNone, JevContentPlan, JevContentPlanSum, JevContentFull, "":
	default:
		return fmt.Errorf("jev.content must be one of %q, got %q",
			[]string{JevContentNone, JevContentPlan, JevContentPlanSum, JevContentFull}, c.Jev.Content)
	}
	return nil
}

// Durations returns the parsed loop timeouts (Validate guarantees they parse).
func (l Loop) Durations() (request, shell time.Duration) {
	request, _ = time.ParseDuration(l.RequestTimeout)
	shell, _ = time.ParseDuration(l.ShellTimeout)
	return request, shell
}
