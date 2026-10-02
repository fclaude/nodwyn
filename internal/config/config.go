// Package config locates nodwyn's files and loads its configuration.
//
// Layout (XDG, also on macOS — nodwyn is a developer tool):
//
//	$XDG_CONFIG_HOME/nodwyn/config.toml     control plane, window budget, policy
//	$XDG_CONFIG_HOME/nodwyn/bindings.toml   the user-owned binding table (P3)
//	$XDG_CONFIG_HOME/nodwyn/keychain        encrypted credential store (P3) — never in the log
//	$XDG_DATA_HOME/nodwyn/sessions/         one .jsonl per session (the log)
//
// Missing files mean defaults; a present file is parsed over the defaults and
// validated. There is no secret in any of the files this package reads.
package config

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/fclaude/nodwyn/internal/detect/l1"
)

// Paths are the directories and files nodwyn uses.
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
	return PathsUnder(filepath.Join(cfg, "nodwyn"), filepath.Join(data, "nodwyn")), nil
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
	// Control is the model a session talks to unless a strategy asks for
	// another: an entry of the same shape as Models, required once a session
	// needs a model (MissingControl). There is no built-in default.
	Control Model  `toml:"control"`
	Window  Window `toml:"window"`
	Policy  Policy `toml:"policy"`
	Loop    Loop   `toml:"loop"`
	Budget  Budget `toml:"budget"`
	Jev     Jev    `toml:"jev"`
	Docs    Docs   `toml:"docs"`
	MCP     MCP    `toml:"mcp"`
	// Models are the models a loop strategy may ask by name, besides the
	// control model: (model-turn "NAME") for [models.NAME].
	Models map[string]Model `toml:"models"`
}

// MCP is how nodwyn runs contained MCP servers (ADR 23 W3b). The servers
// themselves are in mcp.toml.
type MCP struct {
	// Runtime is the container runtime: "podman" or "docker". Unset:
	// podman if it is on PATH, else docker. Without one, contained servers
	// are unavailable and remote ones still work.
	Runtime string `toml:"runtime"`
	// Egress is the nodwyn-egress proxy a server with a network list
	// reaches the network through. Unset: next to the nodwyn executable.
	Egress string `toml:"egress"`
}

// ControlModel is the registry name of the [control] model: (model-turn)
// asks it, and no [models.NAME] may take the name.
const ControlModel = "control"

// Model is one entry of the model registry, or the [control] model.
// Configuring a model is consenting that what a strategy sends it leaves
// the machine. A keyed model also needs its key bound to the URL's host
// (bindings.toml, POST at header:Authorization:Bearer); that is checked on
// every request.
type Model struct {
	Provider   string `toml:"provider"`   // "ollama" (the default when unset), or "openai" for any chat-completions server
	URL        string `toml:"url"`        // base URL; for openai including the version, e.g. https://api.openai.com/v1
	Model      string `toml:"model"`      // model id as the provider names it
	NumCtx     int    `toml:"num_ctx"`    // context length; sent to ollama; for [control] it also sizes the window (Window.Sized)
	KeepAlive  string `toml:"keep_alive"` // ollama only
	Credential string `toml:"credential"` // keychain class of the API key; empty for none
	// MaxOutput caps this model's replies, thinking included, instead of
	// window.output_reserve: a remote reasoning model may need more. 0 uses
	// the window's.
	MaxOutput int `toml:"max_output"`
	// OutputField is the request field a chat-completions server reads the
	// cap from: "max_completion_tokens" (the default; reasoning models
	// refuse the older field) or "max_tokens", for servers that only know
	// that one. openai only.
	OutputField string `toml:"output_field"`
	// Temperature and Seed are sampling settings sent with every request to
	// this model; unset leaves them to the server. Temperature 0 with a
	// seed makes replies as repeatable as the server allows (ollama's
	// options, a chat-completions server's fields).
	Temperature *float64 `toml:"temperature"`
	Seed        *int64   `toml:"seed"`
	// Vision says the model takes images, so an attached image may be sent
	// to it (ADR 22 §3). openai only: a chat-completions server does not
	// say, while ollama reports it (/api/show).
	Vision bool `toml:"vision"`
	// PriceInput, PriceCachedInput and PriceOutput are the model's prices
	// per million tokens, in USD, for estimating what a request cost when
	// the provider does not report it (OpenRouter does). Unset is unknown;
	// cached input unset is priced as input. Nothing is enforced with them.
	PriceInput       *float64 `toml:"price_input"`
	PriceCachedInput *float64 `toml:"price_cached_input"`
	PriceOutput      *float64 `toml:"price_output"`
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
	// ApproveThreshold is the judge's probability of "allow" at which it
	// approves a call when it answers a run's confirms (--judge, ADR 18):
	// above 0, at most 1, default 0.8. It is here, in the user's config and
	// not in any Scheme a repository can ship, so that nothing but the user
	// moves it.
	ApproveThreshold float64 `toml:"approve_threshold"`
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

// Docs configures the document tools (W5, ADR 22 answer 4). doc-export
// needs nothing here: it runs pandoc from PATH. google-doc-upload sends a
// file to Google Drive with the credential of class GoogleCredential, and
// is offered only to a session that starts with one stored; that class
// must be bound to GoogleUploadURL's host (bindings.toml, POST at
// header:Authorization:Bearer) like any other key.
type Docs struct {
	// GoogleCredential is the keychain class of the Google credential;
	// default "google", the class nodwyn credential add --oauth2 google
	// stores. "" offers no Google tool.
	GoogleCredential string `toml:"google_credential"`
	// GoogleUploadURL is Drive's upload endpoint; its host is the
	// destination the binding table judges. Default GoogleUploadURL.
	GoogleUploadURL string `toml:"google_upload_url"`
	// GoogleUpload is how an upload is handled: "ask" (allow by hand: a
	// person says yes to each, and --yolo, --judge or an unattended run
	// never does), "allow" (always, with no confirm; the binding check and
	// the key scan still apply), or "deny" (refused, and the tool is not
	// offered). Unset, the first upload asks the person which, and their
	// answer is saved here.
	GoogleUpload string `toml:"google_upload"`
}

// The google_upload settings.
const (
	UploadAsk   = "ask"
	UploadAllow = "allow"
	UploadDeny  = "deny"
)

// GoogleUploadURL is Google Drive's upload endpoint (Drive API v3).
const GoogleUploadURL = "https://www.googleapis.com/upload/drive/v3/files"

func (d Docs) validate() error {
	switch d.GoogleUpload {
	case "", UploadAsk, UploadAllow, UploadDeny:
	default:
		return fmt.Errorf("docs.google_upload must be %q, %q or %q, got %q", UploadAsk, UploadAllow, UploadDeny, d.GoogleUpload)
	}
	u, err := url.Parse(d.GoogleUploadURL)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("docs.google_upload_url must be an absolute URL without a query, got %q", d.GoogleUploadURL)
	}
	// The access token travels with every upload: https, or plain http to
	// this machine only (a test's fake).
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback(u.Hostname())) {
		return fmt.Errorf("docs.google_upload_url must be https (or http on this machine), got %q", d.GoogleUploadURL)
	}
	return nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Loop bounds one turn of the agent loop.
type Loop struct {
	MaxToolCalls   int    `toml:"max_tool_calls"`  // legacy hard override; zero when absent, --max-tool-calls overrides
	RequestTimeout string `toml:"request_timeout"` // per model request, e.g. "30m"
	ShellTimeout   string `toml:"shell_timeout"`   // per shell-exec, e.g. "10m"
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
	OutputReserve   int `toml:"output_reserve"`    // tokens reserved for generation, and the cap on each reply's (thinking included)
	// Fixed is set when config.toml sets high_water or low_water: the water
	// marks then stay as set, instead of growing with the model's context
	// (Sized).
	Fixed bool `toml:"-"`
}

// Sized is the window for a control model whose context holds context
// tokens (0 when unknown), reserve of them kept for its reply, and the
// ceiling: the most a request may carry, context less reserve. The
// defaults are for a small local context; a larger one raises the high
// water to three quarters of the ceiling and the low water to half of
// that, unless config.toml sets them (Fixed). The quarter left above the
// high water is room for the request that compacts the context, so a long
// task is summarized before anything is trimmed (compact.scm). With the
// context unknown, or no bigger than the window, the ceiling is the high
// water. The window's estimate counts bytes/3, usually more than a
// server's tokenizer, so the ceiling rarely overfills the context.
func (w Window) Sized(context, reserve int) (Window, int) {
	room := context - reserve
	if !w.Fixed && room*3/4 > w.HighWater {
		w.HighWater = room * 3 / 4
		w.LowWater = max(w.LowWater, w.HighWater/2)
	}
	return w, max(room, w.HighWater)
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

// RepoAllow is <repo>/.nodwyn/allow.toml: standing approvals for one
// repository, merged with policy.allow_commands.
type RepoAllow struct {
	Commands []string `toml:"commands"`
	Writes   []string `toml:"writes"`
}

// LoadRepoAllow reads a repository's allow file. Missing means none.
func LoadRepoAllow(path string) (RepoAllow, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return RepoAllow{}, nil
	}
	if err != nil {
		return RepoAllow{}, err
	}
	return ParseRepoAllow(raw)
}

// ParseRepoAllow validates proposed standing approvals with the loading rules.
func ParseRepoAllow(raw []byte) (RepoAllow, error) {
	var r RepoAllow
	md, err := toml.Decode(string(raw), &r)
	if err != nil {
		return RepoAllow{}, fmt.Errorf("allow: %w", err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return RepoAllow{}, fmt.Errorf("allow: unknown keys: %v", undec)
	}
	return r, nil
}

// Default is the configuration nodwyn runs with when no config.toml exists.
func Default() Config {
	return Config{
		Window: Window{HighWater: 60 * 1024, LowWater: 30 * 1024, MaxResultTokens: 3 * 1024, OutputReserve: 16 * 1024},
		Policy: Policy{Unbound: "deny", L1: L1{Enabled: true}},
		Loop:   Loop{MaxToolCalls: 0, RequestTimeout: "30m", ShellTimeout: "10m"},
		Budget: Budget{ToolCallsSoft: 50, ToolCallsHard: 500, TokensSoft: 1_000_000, TokensHard: 10_000_000, ActionsSoft: 25, ActionsHard: 200, WallSoft: "20m", WallHard: "4h"},
		// Jev defaults: no service (nodwyn runs file-only, gate is "plan"), full
		// content when one is configured. A developer points jev.url at their
		// instance to enable the gate (ADR 19).
		Jev:  Jev{URL: "https://api.typesafe.ai", Model: "jev-latest", Timeout: "10s", Content: JevContentPlan, ApproveThreshold: 0.8},
		Docs: Docs{GoogleCredential: "google", GoogleUploadURL: GoogleUploadURL},
	}
}

// Load reads path over Default. A missing file is not an error.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", path, err)
	}
	c, err := Parse(raw)
	if err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", path, err)
	}
	return c, nil
}

// Parse reads config.toml's contents over the defaults and validates them,
// as Load does for the file.
func Parse(raw []byte) (Config, error) {
	c := Default()
	md, err := toml.Decode(string(raw), &c)
	if err != nil {
		return Config{}, err
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return Config{}, fmt.Errorf("unknown keys: %v", undec)
	}
	c.Window.Fixed = md.IsDefined("window", "high_water") || md.IsDefined("window", "low_water")
	if md.IsDefined("loop", "max_tool_calls") && c.Loop.MaxToolCalls <= 0 {
		return Config{}, fmt.Errorf("loop.max_tool_calls must be positive when set")
	}
	if md.IsDefined("loop", "max_tool_calls") && md.IsDefined("budget", "tool_calls_hard") {
		return Config{}, fmt.Errorf("set budget.tool_calls_hard or legacy loop.max_tool_calls, not both")
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate rejects configurations the harness must not run with.
func (c Config) Validate() error {
	if c.Control != (Model{}) {
		need := c.Window.HighWater + c.Window.OutputReserve
		var missing []string
		if c.Control.URL == "" {
			missing = append(missing, "url")
		}
		if c.Control.Model == "" {
			missing = append(missing, "model")
		}
		if len(missing) > 0 {
			verb := "is"
			if len(missing) > 1 {
				verb = "are"
			}
			return fmt.Errorf("control: %s %s required: nodwyn has no built-in control model (it no longer assumes ollama at http://localhost:11434); set provider, url, model and num_ctx in [control]",
				strings.Join(missing, " and "), verb)
		}
		if err := c.Control.validate(); err != nil {
			return fmt.Errorf("control: %w", err)
		}
		// Unset, an ollama model is asked for the window's size (WindowNumCtx)
		// and another server decides its own; set, the window must fit in it.
		if n := c.Control.NumCtx; n > 0 && need > n {
			return fmt.Errorf("window.high_water + window.output_reserve (%d) exceeds control.num_ctx (%d)", need, n)
		}
	}
	if _, ok := c.Models[ControlModel]; ok {
		return fmt.Errorf("models.%s: the name %q is the [control] model", ControlModel, ControlModel)
	}
	for name, m := range c.Models {
		if err := m.validate(); err != nil {
			return fmt.Errorf("models.%s: %w", name, err)
		}
	}
	if c.Window.HighWater <= 0 || c.Window.LowWater <= 0 || c.Window.OutputReserve <= 0 || c.Window.MaxResultTokens <= 0 {
		return errors.New("window values must be positive")
	}
	if c.Window.LowWater >= c.Window.HighWater {
		return fmt.Errorf("window.low_water (%d) must be below window.high_water (%d)", c.Window.LowWater, c.Window.HighWater)
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
	if c.Jev.ApproveThreshold <= 0 || c.Jev.ApproveThreshold > 1 {
		return fmt.Errorf("jev.approve_threshold must be above 0 and at most 1, got %v", c.Jev.ApproveThreshold)
	}
	if err := c.Docs.validate(); err != nil {
		return err
	}
	switch c.Jev.Content {
	case JevContentNone, JevContentPlan, JevContentPlanSum, JevContentFull, "":
	default:
		return fmt.Errorf("jev.content must be one of %q, got %q",
			[]string{JevContentNone, JevContentPlan, JevContentPlanSum, JevContentFull}, c.Jev.Content)
	}
	switch c.MCP.Runtime {
	case "", "podman", "docker":
	default:
		return fmt.Errorf("mcp.runtime must be \"podman\" or \"docker\", got %q", c.MCP.Runtime)
	}
	if c.MCP.Egress != "" && !filepath.IsAbs(c.MCP.Egress) {
		return fmt.Errorf("mcp.egress must be an absolute path, got %q", c.MCP.Egress)
	}
	return nil
}

// Durations returns the parsed loop timeouts (Validate guarantees they parse).
func (l Loop) Durations() (request, shell time.Duration) {
	request, _ = time.ParseDuration(l.RequestTimeout)
	shell, _ = time.ParseDuration(l.ShellTimeout)
	return request, shell
}

func (m Model) validate() error {
	switch m.Provider {
	case "", "ollama", "openai": // unset is ollama
	default:
		return fmt.Errorf("provider %q is not supported (ollama, openai)", m.Provider)
	}
	u, err := url.Parse(m.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("url %q must be an http or https URL", m.URL)
	}
	if m.Model == "" {
		return errors.New("model is required")
	}
	if m.NumCtx < 0 {
		return errors.New("num_ctx must not be negative")
	}
	if m.Provider != "openai" && m.Credential != "" {
		return errors.New("credential is not supported for ollama")
	}
	if m.MaxOutput < 0 {
		return errors.New("max_output must not be negative")
	}
	switch m.OutputField {
	case "", "max_completion_tokens", "max_tokens":
	default:
		return fmt.Errorf("output_field %q is not supported (max_completion_tokens, max_tokens)", m.OutputField)
	}
	if m.Provider != "openai" && m.OutputField != "" {
		return errors.New("output_field is for openai servers; ollama takes num_predict")
	}
	if m.Temperature != nil && (*m.Temperature < 0 || *m.Temperature > 2) {
		return fmt.Errorf("temperature %v must be between 0 and 2", *m.Temperature)
	}
	if m.Provider != "openai" && m.Vision {
		return errors.New("vision is for openai servers; ollama says itself whether a model takes images")
	}
	for name, p := range map[string]*float64{"price_input": m.PriceInput, "price_cached_input": m.PriceCachedInput, "price_output": m.PriceOutput} {
		if p != nil && (*p < 0 || math.IsNaN(*p) || math.IsInf(*p, 0)) {
			return fmt.Errorf("%s %v must be a price of 0 or more (USD per million tokens)", name, *p)
		}
	}
	return nil
}

// WindowNumCtx is the context length an ollama model is asked for when its
// num_ctx is unset: the window's high water plus the output reserve. Left to
// itself Ollama would use its own default, usually far smaller, and cut the
// prompt silently.
func (c Config) WindowNumCtx() int { return c.Window.HighWater + c.Window.OutputReserve }

// MissingControl is the error for a session that needs a model when
// config.toml has no [control]: it says what to add, and where.
func MissingControl(configFile string) error {
	where := "config.toml"
	if configFile != "" {
		where = configFile
	}
	return fmt.Errorf(`no control model is configured; add a [control] table to %s, for example:

[control]
provider = "ollama"               # or "openai" for any chat-completions server
url = "http://localhost:11434"    # for openai, the base URL including /v1
model = "qwen3:8b"
num_ctx = 81920                   # at least window.high_water + window.output_reserve (77824 by default)
# credential = "openai-key"       # keychain class of the API key, bound in bindings.toml`, where)
}
