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
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
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
}

// Loop bounds one turn of the agent loop.
type Loop struct {
	MaxToolCalls   int    `toml:"max_tool_calls"`  // hard stop per user message
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

// Window is the context budget (ADR 9).
type Window struct {
	InputBudget   int `toml:"input_budget"`   // tokens of prefix + messages we allow ourselves
	OutputReserve int `toml:"output_reserve"` // tokens reserved for generation
}

// Policy holds the guard's user-settable defaults (ADR 6).
type Policy struct {
	Unbound string `toml:"unbound"` // "deny" (default) or "confirm" for unbound secret-bearing egress
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
		Window: Window{InputBudget: 80 * 1024, OutputReserve: 8 * 1024},
		Policy: Policy{Unbound: "deny"},
		Loop:   Loop{MaxToolCalls: 50, RequestTimeout: "30m", ShellTimeout: "10m"},
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
	if c.Window.InputBudget <= 0 || c.Window.OutputReserve <= 0 {
		return errors.New("window budgets must be positive")
	}
	if c.Window.InputBudget+c.Window.OutputReserve > c.Control.NumCtx {
		return fmt.Errorf("window.input_budget + window.output_reserve (%d) exceeds control.num_ctx (%d)",
			c.Window.InputBudget+c.Window.OutputReserve, c.Control.NumCtx)
	}
	switch c.Policy.Unbound {
	case "deny", "confirm":
	default:
		return fmt.Errorf("policy.unbound must be \"deny\" or \"confirm\", got %q", c.Policy.Unbound)
	}
	if c.Loop.MaxToolCalls <= 0 {
		return errors.New("loop.max_tool_calls must be positive")
	}
	for name, v := range map[string]string{"loop.request_timeout": c.Loop.RequestTimeout, "loop.shell_timeout": c.Loop.ShellTimeout} {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			return fmt.Errorf("%s must be a positive duration, got %q", name, v)
		}
	}
	return nil
}

// Durations returns the parsed loop timeouts (Validate guarantees they parse).
func (l Loop) Durations() (request, shell time.Duration) {
	request, _ = time.ParseDuration(l.RequestTimeout)
	shell, _ = time.ParseDuration(l.ShellTimeout)
	return request, shell
}

// Binding is one row of the binding table (SPEC "Egress authority"):
// a secret class is authorized for exactly these destinations, positions
// and methods. Anything else is unbound.
type Binding struct {
	Class     string   `toml:"class"`     // e.g. "openai-key"
	Dest      string   `toml:"dest"`      // host, e.g. "api.openai.com"
	Positions []string `toml:"positions"` // e.g. "header:Authorization", "query:key", "body"
	Methods   []string `toml:"methods"`   // e.g. "POST"
}

// Bindings is bindings.toml.
type Bindings struct {
	Bind []Binding `toml:"bind"`
}

// LoadBindings reads the binding table. A missing file is an empty table,
// which by ADR 6 means every secret-bearing egress is denied.
func LoadBindings(path string) (Bindings, error) {
	var b Bindings
	md, err := toml.DecodeFile(path, &b)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Bindings{}, nil
		}
		return Bindings{}, fmt.Errorf("bindings: %s: %w", path, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return Bindings{}, fmt.Errorf("bindings: %s: unknown keys: %v", path, undec)
	}
	for i, row := range b.Bind {
		if row.Class == "" || row.Dest == "" || len(row.Positions) == 0 || len(row.Methods) == 0 {
			return Bindings{}, fmt.Errorf("bindings: %s: row %d: class, dest, positions and methods are all required", path, i+1)
		}
	}
	return b, nil
}
