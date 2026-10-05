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
	"maps"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fclaude/nodwyn/internal/catalogue"

	"github.com/fclaude/nodwyn/internal/detect/l1"
	"github.com/fclaude/nodwyn/internal/llm"
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
	// Approval is what a session a person can answer does without asking
	// (ADR 35).
	Approval Approval `toml:"approval"`
	// Models are the models a loop strategy may ask by name, besides the
	// control model: (model-turn "NAME") for [models.NAME].
	Models map[string]Model `toml:"models"`
	// Servers are the model servers models may name instead of giving an
	// address and a key each: [servers.NAME], so the models one provider
	// offers share its address and its key.
	Servers map[string]Server `toml:"servers"`
	// Catalogue is what providers publish about their models, read for what
	// a model's entry leaves unset (Published); nil is the snapshot nodwyn
	// was built with. It is never read from or written to config.toml.
	Catalogue *catalogue.Catalogue `toml:"-"`
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

// Approval is what a session a person can answer does without asking
// them: the console's, the TUI's, the plain front-end's and a supervised
// run's (ADR 35). nodwyn run's flags and --no-confirm replace it for their
// own sessions; writes applies to every session.
// Neither setting waives a security confirm or relaxes a deny.
type Approval struct {
	// Writes is "allow" (the default: a write the pin lets through goes
	// ahead, its diff logged and its previous content kept) or "ask".
	Writes string `toml:"writes"`
	// Commands is who answers any other ordinary confirm no standing
	// approval covers — a command, input to a running program, an MCP tool
	// that acts: "judge" (the default: the judge, and the person when it
	// refuses or there is none), "ask" (the person) or "allow" (yes, as
	// --yolo answers).
	Commands string `toml:"commands"`
	// DontAsk offers each task the "Don't ask" switch, which answers every
	// ordinary write and command yes until it is turned off. Off by
	// default, and nothing but a person in Settings turns it on: in the
	// session logs of 2026-09-30 to 10-05 the switch was on in 40 of 75
	// sessions, often seconds after the first question, and under it the
	// model loaded the person's SSH key into an agent, read a file the
	// guard had refused through the shell and rebased the person's own
	// checkout, none of it asked (ADR 17, amended 2026-10-05).
	DontAsk bool `toml:"dont_ask"`
}

// The approval.writes and approval.commands settings.
const (
	ApproveAllow = "allow"
	ApproveAsk   = "ask"
	ApproveJudge = "judge"
)

func (a Approval) validate() error {
	switch a.Writes {
	case ApproveAllow, ApproveAsk:
	default:
		return fmt.Errorf("approval.writes must be %q or %q, got %q", ApproveAllow, ApproveAsk, a.Writes)
	}
	switch a.Commands {
	case ApproveJudge, ApproveAsk, ApproveAllow:
	default:
		return fmt.Errorf("approval.commands must be %q, %q or %q, got %q", ApproveJudge, ApproveAsk, ApproveAllow, a.Commands)
	}
	return nil
}

// ControlModel is the registry name of the [control] model: (model-turn)
// asks it, and no [models.NAME] may take the name.
const ControlModel = "control"

// Model is one entry of the model registry, or the [control] model.
// Configuring a model is consenting that what a strategy sends it leaves
// the machine. A keyed model also needs its key bound to the URL's host
// (bindings.toml, POST at KeyPosition: a bearer header, or x-api-key for
// Anthropic); that is checked on every request.
type Model struct {
	// Server names the [servers.NAME] the model is on. Parse copies the
	// server's provider, dialect, URL and credential into the model, so the
	// rest of nodwyn reads a model alone; a model that names a server sets none of
	// these itself.
	Server     string          `toml:"server"`
	Dialect    llm.ChatDialect `toml:"dialect" json:",omitempty"` // Chat Completions dialect; inherited from Server when named.
	Provider   string          `toml:"provider"`                  // "ollama" (the default when unset), "openai" for any chat-completions server, or "anthropic" for Claude's Messages API
	URL        string          `toml:"url"`                       // base URL; for openai including the version, e.g. https://api.openai.com/v1
	Model      string          `toml:"model"`                     // model id as the provider names it
	NumCtx     int             `toml:"num_ctx"`                   // context length; sent to ollama; for [control] it also sizes the window (Window.Sized)
	KeepAlive  string          `toml:"keep_alive"`                // ollama only
	Credential string          `toml:"credential"`                // keychain class of the API key; empty for none
	// MaxOutput caps this model's replies, thinking included, and is the
	// room the window keeps for one: a remote reasoning model may need more
	// than the default. It must be below NumCtx. 0 is Window.ReplyCap's
	// default.
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
	// Vision says whether the model takes images, so an attached image may
	// be sent to it (ADR 22 §3). openai and anthropic only: those servers do
	// not say, while ollama reports it (/api/show). Unset, it is what the
	// model's provider publishes (WithPublished), and for a model nothing is
	// published of, the model is taken to (TakesImages): a provider that
	// refuses an image fails that request, which costs less than refusing an
	// image a person has prepared a message around (ADR 22, amended
	// 2026-10-05). true and false say so whatever is published.
	Vision *bool `toml:"vision"`
	// CacheKey is how requests name their session to a chat-completions
	// server, so one that routes by it keeps a session on the replica that
	// holds its prompt cache (ADR 9): "session" (the default) sends a key
	// derived from the session id as the X-Session-Id header and as
	// prompt_cache_key, "header" only as the header, for a server that
	// refuses fields it does not know, and "off" sends neither. openai only.
	CacheKey string `toml:"cache_key"`
	// CacheControl asks a chat-completions provider to cache the prompt:
	// "ephemeral" for the provider's own lifetime (five minutes) or "1h". A
	// Claude model caches a prompt only when it is asked to, unlike the models
	// that cache the start of one by themselves (ADR 9), so a model that
	// needs it says so here; nothing is inferred from its name. Sent as the
	// request's top-level cache_control, which OpenRouter and the providers it
	// routes Claude to read, and which a provider that does not know it may
	// refuse. Anthropic's own API reads it the same way and caches nothing
	// without it. openai and anthropic only.
	CacheControl string `toml:"cache_control"`
	// PriceInput, PriceCachedInput and PriceOutput are the model's prices
	// per million tokens, in USD, for estimating what a request cost when
	// the provider does not report it (OpenRouter does). Unset is what the
	// provider publishes (Rates), else unknown; cached input unset is then
	// priced as input. Nothing is enforced with them.
	PriceInput       *float64 `toml:"price_input"`
	PriceCachedInput *float64 `toml:"price_cached_input"`
	PriceOutput      *float64 `toml:"price_output"`
	// Efforts are the reasoning efforts the model takes, in the words its
	// server accepts: ["low", "medium", "high"], ["minimal", "low", "medium",
	// "high"], or ["off", "on"] for an Ollama model that thinks or does not
	// (ADR 41). Nothing is inferred from the model's name or the server's
	// address, so a model that declares none takes no effort setting and is
	// sent none. Efforts may be left off a model that another entry in
	// config.toml declares them for (Config.Efforts).
	Efforts []string `toml:"efforts"`
	// Effort is the effort a conversation on this model starts with when
	// nobody chose one: one of Efforts. Unset sends none, which leaves the
	// model to its provider's default. For [control] it is the effort of the
	// default model, which a new task starts with.
	Effort string `toml:"effort"`
}

// TakesImages says whether an image may be sent to m, a chat-completions
// model: unless its entry, or what its provider publishes once
// WithPublished has filled it in, says it does not (Vision).
func (m Model) TakesImages() bool { return m.Vision == nil || *m.Vision }

// isZero says whether m is the empty model, as an unset [control] is. Model
// holds a slice, so it is not comparable with ==.
func (m Model) isZero() bool { return reflect.DeepEqual(m, Model{}) }

// Server is a model server: where its models are reached and the key they
// are reached with. A provider offers many models at one address under one
// key, so the models on it name the server rather than repeat them.
type Server struct {
	Dialect    llm.ChatDialect `toml:"dialect"`    // Empty keeps the compatible dialect; never inferred from the URL.
	Provider   string          `toml:"provider"`   // as Model.Provider
	URL        string          `toml:"url"`        // as Model.URL
	Credential string          `toml:"credential"` // as Model.Credential
}

// The cache_control settings.
const (
	CacheControlEphemeral = "ephemeral"
	CacheControlHour      = "1h"
)

// The cache_key settings.
const (
	CacheKeySession = "session"
	CacheKeyHeader  = "header"
	CacheKeyOff     = "off"
)

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
	// PriceInput and PriceOutput are the judge's prices, USD per million
	// tokens: the API reports what a request used, never what it cost, so
	// these estimate it. Unset, judge requests are counted, not priced.
	PriceInput  *float64 `toml:"price_input"`
	PriceOutput *float64 `toml:"price_output"`
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
	OutputReserve   int `toml:"output_reserve"`    // the reply cap of a model that sets no max_output and has none published (ReplyCap)
	// Fixed is set when config.toml sets high_water or low_water: the water
	// marks then stay as set, instead of growing with the model's context
	// (Sized).
	Fixed bool `toml:"-"`
	// ResultsFixed is set when config.toml sets max_result_tokens: the cap
	// then stays as set, instead of growing with the window (Sized).
	ResultsFixed bool `toml:"-"`
}

// The cap on one tool result grows with the window it is shown in, a
// resultShare-th of the high water, up to maxResultCap. At the default
// 3,072 a whole-file read of more than about 9 KB lost its middle whatever
// the window held: 496 of 3,701 reads (13%) in the local session logs of
// 2026-09-30 to 10-05, with a 654k or 774k high water, 126 of them read
// again in ranges straight after (ADR 9, amended 2026-10-05).
const (
	resultShare  = 48
	maxResultCap = 16384
)

// Sized is the window for a control model whose context holds context
// tokens (0 when unknown), reserve of them kept for its reply (ReplyCap),
// and the ceiling: the most a request may carry, context less reserve. The
// defaults are for a small local context; a larger one raises the high
// water to three quarters of the ceiling and the low water to half of
// that, unless config.toml sets them (Fixed). A context too small for the
// water marks, set or not, lowers them the same way: a window the model
// cannot hold would have every request past the high water refused. The
// quarter left above the high water is room for the request that compacts
// the context, so a long task is summarized before anything is trimmed
// (compact.scm). A window that grows grows its cap on one tool result too,
// to a resultShare-th of its high water, unless config.toml sets the cap
// (ResultsFixed). With the context unknown, or leaving no room beside the
// reply, the window stays and the ceiling is the high water. The window's
// estimate counts bytes/3, usually more than a server's tokenizer, so the
// ceiling rarely overfills the context.
func (w Window) Sized(context, reserve int) (Window, int) {
	room := context - reserve
	switch {
	case room <= 0:
	case !w.Fixed && room*3/4 > w.HighWater:
		w.HighWater = room * 3 / 4
		w.LowWater = max(w.LowWater, w.HighWater/2)
		if !w.ResultsFixed {
			w.MaxResultTokens = max(w.MaxResultTokens, min(w.HighWater/resultShare, maxResultCap))
		}
	case w.HighWater > room:
		w.HighWater = room * 3 / 4
		w.LowWater = min(w.LowWater, w.HighWater/2)
	}
	return w, max(room, w.HighWater)
}

// ReplyCap is the most one reply of a model may take, thinking included,
// and the room the window keeps for it: the model's max_output when its
// entry sets one or its provider publishes one (WithPublished fills it in),
// else output_reserve, the fallback. A context known to hold context
// tokens (0 when unknown) that is too small for the window and that reply
// gives it no more than a quarter, so a small model keeps most of its
// context for the conversation. A context that holds both keeps
// output_reserve, which an Ollama model asked for exactly that much
// (WindowNumCtx) depends on.
func (w Window) ReplyCap(context, maxOutput int) int {
	if maxOutput > 0 {
		return maxOutput
	}
	if context > 0 && context < w.HighWater+w.OutputReserve {
		return min(w.OutputReserve, context/4)
	}
	return w.OutputReserve
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
		// The soft limits are the hard ones: no checkpoint interrupts a turn
		// unless the person asks for one (ADR 35).
		Budget: Budget{ToolCallsSoft: 500, ToolCallsHard: 500, TokensSoft: 10_000_000, TokensHard: 10_000_000, ActionsSoft: 200, ActionsHard: 200, WallSoft: "4h", WallHard: "4h"},
		// Jev defaults: no service (nodwyn runs file-only, gate is "plan"), full
		// content when one is configured. A developer points jev.url at their
		// instance to enable the gate (ADR 19).
		Jev:      Jev{URL: "https://api.typesafe.ai", Model: "jev-latest", Timeout: "10s", Content: JevContentPlan, ApproveThreshold: 0.8},
		Docs:     Docs{GoogleCredential: "google", GoogleUploadURL: GoogleUploadURL},
		Approval: Approval{Writes: ApproveAllow, Commands: ApproveJudge},
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
	// [tasks] chose how the console started a task (ADR 18); a chat is the
	// only way now (ADR 35), and a file that still has the table loads.
	undec := slices.DeleteFunc(md.Undecoded(), func(k toml.Key) bool { return len(k) > 0 && k[0] == "tasks" })
	if len(undec) > 0 {
		return Config{}, fmt.Errorf("unknown keys: %v", undec)
	}
	c.Budget.softFollowsHard(md)
	c.Window.Fixed = md.IsDefined("window", "high_water") || md.IsDefined("window", "low_water")
	c.Window.ResultsFixed = md.IsDefined("window", "max_result_tokens")
	if md.IsDefined("loop", "max_tool_calls") && c.Loop.MaxToolCalls <= 0 {
		return Config{}, fmt.Errorf("loop.max_tool_calls must be positive when set")
	}
	if md.IsDefined("loop", "max_tool_calls") && md.IsDefined("budget", "tool_calls_hard") {
		return Config{}, fmt.Errorf("set budget.tool_calls_hard or legacy loop.max_tool_calls, not both")
	}
	if err := c.resolveServers(); err != nil {
		return Config{}, err
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// resolveServers gives each model that names a server the server's
// provider, dialect, URL and credential. A model that also sets one of them is
// refused rather than one of the two winning: either reading would surprise
// whoever wrote the other.
func (c *Config) resolveServers() error {
	resolve := func(where string, m *Model) error {
		if m.Server == "" {
			return nil
		}
		s, ok := c.Servers[m.Server]
		if !ok {
			return fmt.Errorf("%s: server %q is not configured: add a [servers.%s] table with its provider, url and credential", where, m.Server, m.Server)
		}
		for _, f := range []struct{ key, value string }{{"provider", m.Provider}, {"dialect", string(m.Dialect)}, {"url", m.URL}, {"credential", m.Credential}} {
			if f.value != "" {
				return fmt.Errorf("%s: %s comes from server %q; set it in [servers.%s], not here", where, f.key, m.Server, m.Server)
			}
		}
		m.Provider, m.Dialect, m.URL, m.Credential = s.Provider, s.Dialect, s.URL, s.Credential
		return nil
	}
	if err := resolve("control", &c.Control); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(c.Models)) {
		m := c.Models[name]
		if err := resolve("models."+name, &m); err != nil {
			return err
		}
		c.Models[name] = m
	}
	return nil
}

// Validate rejects configurations the harness must not run with.
func (c Config) Validate() error {
	if !c.Control.isZero() {
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
			return fmt.Errorf("control: %s %s required: nodwyn has no built-in control model (it no longer assumes ollama at http://localhost:11434); set provider, url, model and num_ctx in [control], or a server from [servers] and a model",
				strings.Join(missing, " and "), verb)
		}
		if err := c.Control.validate(); err != nil {
			return fmt.Errorf("control: %w", err)
		}
		if err := c.checkEffort(c.Control); err != nil {
			return fmt.Errorf("control: %w", err)
		}
	}
	if _, ok := c.Models[ControlModel]; ok {
		return fmt.Errorf("models.%s: the name %q is the [control] model", ControlModel, ControlModel)
	}
	for name, m := range c.Models {
		if err := m.validate(); err != nil {
			return fmt.Errorf("models.%s: %w", name, err)
		}
		if err := c.checkEffort(m); err != nil {
			return fmt.Errorf("models.%s: %w", name, err)
		}
	}
	for name, s := range c.Servers {
		if err := s.validate(); err != nil {
			return fmt.Errorf("servers.%s: %w", name, err)
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
	for name, p := range map[string]*float64{"price_input": c.Jev.PriceInput, "price_output": c.Jev.PriceOutput} {
		if p != nil && (math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0) {
			return fmt.Errorf("jev.%s must be US dollars per million tokens, at least 0, got %v", name, *p)
		}
	}
	if err := c.Docs.validate(); err != nil {
		return err
	}
	if err := c.Approval.validate(); err != nil {
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

// The positions a model's key goes at: the chat-completions family takes it
// as a bearer token, Anthropic's API in its own header, with no scheme.
const (
	KeyPositionBearer    = "header:Authorization:Bearer"
	KeyPositionAnthropic = "header:x-api-key"
)

// KeyPosition is where a keyed model's key goes in its requests, which is the
// position its binding must allow: the same for every request, since the
// binding names the host, the method and this.
func KeyPosition(provider string) string {
	if provider == "anthropic" {
		return KeyPositionAnthropic
	}
	return KeyPositionBearer
}

// isOllama says whether provider is Ollama, which is what an unset one means.
func isOllama(provider string) bool { return provider == "" || provider == "ollama" }

// validate checks a server's own settings, which a model that does not
// name a server sets itself.
func (s Server) validate() error {
	switch s.Provider {
	case "", "ollama", "openai", "anthropic": // unset is ollama
	default:
		return fmt.Errorf("provider %q is not supported (ollama, openai, anthropic)", s.Provider)
	}
	if err := s.Dialect.Validate(); err != nil {
		return err
	}
	if s.Provider != "openai" && s.Dialect != "" {
		return errors.New("dialect is for openai servers; ollama and anthropic use their own protocols")
	}
	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("url %q must be an http or https URL", s.URL)
	}
	if isOllama(s.Provider) && s.Credential != "" {
		return errors.New("credential is not supported for ollama")
	}
	return nil
}

func (m Model) validate() error {
	if err := (Server{Provider: m.Provider, Dialect: m.Dialect, URL: m.URL, Credential: m.Credential}).validate(); err != nil {
		return err
	}
	if m.Model == "" {
		return errors.New("model is required")
	}
	if m.NumCtx < 0 {
		return errors.New("num_ctx must not be negative")
	}
	if m.MaxOutput < 0 {
		return errors.New("max_output must not be negative")
	}
	if m.NumCtx > 0 && m.MaxOutput >= m.NumCtx {
		return fmt.Errorf("max_output (%d) must be below num_ctx (%d): the reply comes out of the context", m.MaxOutput, m.NumCtx)
	}
	switch m.OutputField {
	case "", "max_completion_tokens", "max_tokens":
	default:
		return fmt.Errorf("output_field %q is not supported (max_completion_tokens, max_tokens)", m.OutputField)
	}
	if m.Provider != "openai" && m.OutputField != "" {
		return errors.New("output_field is for openai servers; ollama takes num_predict and anthropic max_tokens")
	}
	if m.Temperature != nil && (*m.Temperature < 0 || *m.Temperature > 2) {
		return fmt.Errorf("temperature %v must be between 0 and 2", *m.Temperature)
	}
	if m.Provider == "anthropic" && m.Temperature != nil && *m.Temperature > 1 {
		return fmt.Errorf("temperature %v must be between 0 and 1 for anthropic", *m.Temperature)
	}
	if m.Provider == "anthropic" && m.Seed != nil {
		return errors.New("seed is not supported for anthropic: the Messages API has none")
	}
	if isOllama(m.Provider) && m.Vision != nil {
		return errors.New("vision is for openai and anthropic servers; ollama says itself whether a model takes images")
	}
	switch m.CacheKey {
	case "", CacheKeySession, CacheKeyHeader, CacheKeyOff:
	default:
		return fmt.Errorf("cache_key %q is not supported (session, header, off)", m.CacheKey)
	}
	if m.Provider != "openai" && m.CacheKey != "" {
		return errors.New("cache_key is for openai servers")
	}
	switch m.CacheControl {
	case "", CacheControlEphemeral, CacheControlHour:
	default:
		return fmt.Errorf("cache_control %q is not supported (%s, %s)", m.CacheControl, CacheControlEphemeral, CacheControlHour)
	}
	if isOllama(m.Provider) && m.CacheControl != "" {
		return errors.New("cache_control is for openai and anthropic servers; ollama keeps its own prompt cache")
	}
	for name, p := range map[string]*float64{"price_input": m.PriceInput, "price_cached_input": m.PriceCachedInput, "price_output": m.PriceOutput} {
		if p != nil && (*p < 0 || math.IsNaN(*p) || math.IsInf(*p, 0)) {
			return fmt.Errorf("%s %v must be a price of 0 or more (USD per million tokens)", name, *p)
		}
	}
	seen := map[string]bool{}
	for _, e := range m.Efforts {
		if err := llm.ValidEffort(e); err != nil {
			return fmt.Errorf("efforts: %w", err)
		}
		if seen[e] {
			return fmt.Errorf("efforts: %q is listed twice", e)
		}
		seen[e] = true
	}
	if m.Effort != "" {
		if err := llm.ValidEffort(m.Effort); err != nil {
			return err
		}
	}
	return nil
}

// checkEffort says whether m's default effort is one the model takes. A
// model that declares no efforts, here or on another entry for the same
// model, cannot have a default: sending an effort to a model that does not
// take one is how a request is refused.
func (c Config) checkEffort(m Model) error {
	if m.Effort == "" {
		return nil
	}
	efforts := c.Efforts(m)
	switch {
	case len(efforts) == 0:
		return fmt.Errorf("effort %q is set, but the model declares no efforts: add efforts = [...] to it, naming the words its server accepts", m.Effort)
	case !slices.Contains(efforts, m.Effort):
		return fmt.Errorf("effort %q is not one the model takes (%s)", m.Effort, strings.Join(efforts, ", "))
	}
	return nil
}

// Resolve completes a model that names a server with the server's provider,
// dialect, URL and credential, which Parse copies into every model naming one:
// a model built in memory resolves the same way as one read from config.toml.
func (c Config) Resolve(m Model) Model {
	srv, ok := c.Servers[m.Server]
	if m.Server == "" || !ok {
		return m
	}
	if m.URL == "" {
		m.URL = srv.URL
	}
	if m.Provider == "" {
		m.Provider = srv.Provider
	}
	if m.Dialect == "" {
		m.Dialect = srv.Dialect
	}
	if m.Credential == "" {
		m.Credential = srv.Credential
	}
	return m
}

// SameModel says whether a and b are one model at one address: the same
// server when both name one, else the same provider and URL (a trailing slash
// does not matter), and the same model id. Two entries in config.toml may describe one, such as [control]
// and the [models.NAME] it was chosen from; each is resolved first.
func (c Config) SameModel(a, b Model) bool {
	a, b = c.Resolve(a), c.Resolve(b)
	provider := func(m Model) string {
		if m.Provider == "" {
			return "ollama"
		}
		return m.Provider
	}
	if a.Model == "" || a.Model != b.Model {
		return false
	}
	// Servers are keyed by name, so two at one address (an API key and a
	// ChatGPT plan) are different models even when the address is the same.
	if a.Server != "" && b.Server != "" {
		return a.Server == b.Server
	}
	return provider(a) == provider(b) && strings.TrimRight(a.URL, "/") == strings.TrimRight(b.URL, "/")
}

// Tuned is m, a model that is not itself a [models.NAME] entry ([control], or
// a model chosen by its server and id), with each setting it leaves unset
// (num_ctx, keep_alive, max_output, output_field, temperature, seed, vision,
// cache_key, cache_control and the price_* entries) taken from the first
// [models.NAME], in name order, for the same model at the same address. A
// model's settings are said once, in its own entry (ADR 41, amended
// 2026-10-05), so the [control] that names it as the default cannot hold a
// copy that goes stale beside it. What m sets itself stands. An entry has
// its own settings and borrows none: two entries for one model, such as one
// with a smaller context, are two sets of settings.
func (c Config) Tuned(m Model) Model {
	for _, name := range slices.Sorted(maps.Keys(c.Models)) {
		if o := c.Models[name]; c.SameModel(m, o) {
			return m.WithSettings(o)
		}
	}
	return m
}

// WithSettings is m with each setting it leaves unset taken from o.
func (m Model) WithSettings(o Model) Model {
	if m.NumCtx == 0 {
		m.NumCtx = o.NumCtx
	}
	if m.KeepAlive == "" {
		m.KeepAlive = o.KeepAlive
	}
	if m.MaxOutput == 0 {
		m.MaxOutput = o.MaxOutput
	}
	if m.OutputField == "" {
		m.OutputField = o.OutputField
	}
	if m.Temperature == nil {
		m.Temperature = o.Temperature
	}
	if m.Seed == nil {
		m.Seed = o.Seed
	}
	if m.Vision == nil {
		m.Vision = o.Vision
	}
	if m.CacheKey == "" {
		m.CacheKey = o.CacheKey
	}
	if m.CacheControl == "" {
		m.CacheControl = o.CacheControl
	}
	if m.PriceInput == nil {
		m.PriceInput = o.PriceInput
	}
	if m.PriceCachedInput == nil {
		m.PriceCachedInput = o.PriceCachedInput
	}
	if m.PriceOutput == nil {
		m.PriceOutput = o.PriceOutput
	}
	return m
}

// Choice is m without its settings and its efforts: which model it is, where,
// and the effort it starts at. It is what [control] keeps of a model that has
// an entry of its own, which holds the rest (Tuned, Efforts).
func (m Model) Choice() Model {
	return Model{Server: m.Server, Provider: m.Provider, Dialect: m.Dialect, URL: m.URL, Credential: m.Credential, Model: m.Model, Effort: m.Effort}
}

// Efforts are the efforts m takes: its own, else those declared by another
// entry in config.toml for the same model at the same address ([control] first,
// then the [models.NAME] in name order). A person who lists a model's efforts
// once is not asked to say them again where it is the default. Nil is a model
// that takes no effort setting.
func (c Config) Efforts(m Model) []string {
	if len(m.Efforts) > 0 {
		return m.Efforts
	}
	if len(c.Control.Efforts) > 0 && c.SameModel(m, c.Control) {
		return c.Control.Efforts
	}
	for _, name := range slices.Sorted(maps.Keys(c.Models)) {
		if other := c.Models[name]; len(other.Efforts) > 0 && c.SameModel(m, other) {
			return other.Efforts
		}
	}
	if p, ok := c.Published(m); ok {
		return p.Efforts
	}
	return nil
}

// Published is what m's provider publishes about it (internal/catalogue),
// found by the host of its address and its id; false for a model the
// catalogue does not describe, an Ollama one included.
func (c Config) Published(m Model) (catalogue.Model, bool) {
	m = c.Resolve(m)
	if isOllama(m.Provider) {
		return catalogue.Model{}, false
	}
	return c.catalogue().Lookup(m.URL, m.Model)
}

// Like is what the catalogue's providers publish about a model whose id is
// spelled like model, at any of them, and the host whose context that is
// (catalogue.Like): for a server whose own list does not say. It holds the
// context and the reply cap only; prices and efforts are each provider's.
func (c Config) Like(model string) (catalogue.Model, string, bool) {
	return c.catalogue().Like(model)
}

// catalogue is the catalogue models are described by: Catalogue, else the
// snapshot nodwyn was built with.
func (c Config) catalogue() *catalogue.Catalogue {
	if c.Catalogue != nil {
		return c.Catalogue
	}
	return catalogue.Default()
}

// JevPrices are the judge's prices, USD per million tokens: each [jev]'s own
// when it sets it, else what its provider publishes for jev.model at jev.url
// (internal/catalogue); nil when either is known to neither, and the judge's
// requests are counted, not priced. A price set alone is not dropped for
// the published pair, which Settings' empty fields promise.
func (c Config) JevPrices() (input, output *float64) {
	input, output = c.Jev.PriceInput, c.Jev.PriceOutput
	if m, ok := c.catalogue().Lookup(c.Jev.URL, c.Jev.Model); ok && m.Price != nil {
		if input == nil {
			input = &m.Price.Input
		}
		if output == nil {
			output = &m.Price.Output
		}
	}
	if input == nil || output == nil {
		return nil, nil
	}
	return input, output
}

// Rates are the prices m's requests are estimated at (ADR 41, amended
// 2026-10-05): each of its entry's price_* when set, else what its provider
// publishes, so a price typed alone is kept and the others are still known.
// The published cache writes and a long prompt's tiers apply only while the
// entry sets no price: a price typed is the one meant, whatever the prompt's
// length, and those are then priced as input.
func (c Config) Rates(m Model) llm.Rates {
	var r llm.Rates
	if p, ok := c.Published(m); ok && p.Price != nil {
		r = llm.Rates{Input: &p.Price.Input, Output: &p.Price.Output, CachedInput: p.Price.CacheRead, CacheWrite: p.Price.CacheWrite}
		for _, t := range p.Price.Tiers {
			r.Tiers = append(r.Tiers, llm.RateTier{Above: t.Above, Input: &t.Input, Output: &t.Output, CachedInput: t.CacheRead, CacheWrite: t.CacheWrite})
		}
	}
	if m.PriceInput == nil && m.PriceCachedInput == nil && m.PriceOutput == nil {
		return r
	}
	own := llm.Rates{Input: r.Input, CachedInput: r.CachedInput, Output: r.Output}
	if m.PriceInput != nil {
		own.Input = m.PriceInput
	}
	if m.PriceCachedInput != nil {
		own.CachedInput = m.PriceCachedInput
	}
	if m.PriceOutput != nil {
		own.Output = m.PriceOutput
	}
	return own
}

// WithPublished is m with what its entry leaves unset and its provider
// publishes filled in (ADR 41, amended 2026-10-05): its context length, its
// reply cap, which is the published limit on a reply but no more than a
// quarter of the context, so the conversation keeps most of it, and that it
// takes no images when it reads none. What the entry sets stands. Its
// efforts and prices are read where they are used (Efforts, and the rates a
// session prices its requests at).
func (c Config) WithPublished(m Model) Model {
	p, ok := c.Published(m)
	if !ok {
		return m
	}
	if m.NumCtx == 0 {
		m.NumCtx = p.Context
	}
	if m.MaxOutput == 0 && p.Output > 0 {
		m.MaxOutput = p.Output
		if m.NumCtx > 0 {
			m.MaxOutput = min(p.Output, m.NumCtx/4)
		}
	}
	if m.Vision == nil && !p.Images {
		no := false
		m.Vision = &no
	}
	return m
}

// WindowNumCtx is the context length an ollama model whose entry says
// maxOutput (0 unset) is asked for when its num_ctx is unset: the window's
// high water plus the model's reply cap. Left to itself Ollama would use its
// own default, usually far smaller, and cut the prompt silently.
func (c Config) WindowNumCtx(maxOutput int) int {
	return c.Window.HighWater + c.Window.ReplyCap(0, maxOutput)
}

// MissingControl is the error for a session that needs a model when
// config.toml has no [control]: it says what to add, and where.
func MissingControl(configFile string) error {
	where := "config.toml"
	if configFile != "" {
		where = configFile
	}
	return fmt.Errorf(`no control model is configured; add a [control] table to %s, for example:

[control]
provider = "ollama"               # or "openai" for any chat-completions server, or "anthropic"
url = "http://localhost:11434"    # for openai and anthropic, the base URL including /v1
model = "qwen3:8b"
num_ctx = 81920                   # at least window.high_water + window.output_reserve (77824 by default)
# credential = "openai-key"       # keychain class of the API key, bound in bindings.toml`, where)
}
