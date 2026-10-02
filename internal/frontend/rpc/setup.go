package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/policyfile"
	"github.com/fclaude/nodwyn/internal/prim"
)

// The methods in this file let a person set nodwyn up without a terminal
// (ADR 30): unlock or create the keychain, and choose the model. They act
// on the daemon's own configuration, so they take effect for tasks opened
// afterwards; tasks already open keep what they started with.

type keychainStatus struct {
	Exists      bool `json:"exists"`
	Unlocked    bool `json:"unlocked"`
	Credentials int  `json:"credentials"`
}
type passphraseParams struct {
	Passphrase string `json:"passphrase"`
}

func (s *server) keychainStatus() keychainStatus {
	status := keychainStatus{}
	if _, err := os.Stat(s.options.Paths.Keychain); err == nil {
		status.Exists = true
	}
	if s.options.Keychain != nil {
		status.Unlocked = true
		status.Credentials = s.options.Keychain.Count()
	}
	return status
}

func (s *server) keychain(req request, _ *peer) (any, *rpcError) {
	if req.Method == "keychain.status" {
		if err := params(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		return s.keychainStatus(), nil
	}
	var p passphraseParams
	if err := params(req.Params, &p, "passphrase"); err != nil {
		return nil, err
	}
	pass := []byte(p.Passphrase)
	defer clear(pass)
	if len(pass) == 0 {
		return nil, failure(-32602, "A passphrase is required")
	}
	if s.options.Keychain != nil {
		return nil, failure(-32000, "The keychain is already unlocked")
	}
	path := s.options.Paths.Keychain
	var store *keychain.Store
	var err error
	if req.Method == "keychain.create" {
		if _, statErr := os.Stat(path); statErr == nil {
			return nil, failure(-32000, "A keychain already exists; unlock it instead")
		}
		if len(pass) < 8 {
			return nil, failure(-32602, "Use a passphrase of at least 8 characters")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, failure(-32000, err.Error())
		}
		store, err = keychain.Create(path, keychain.DefaultKDF, pass)
	} else {
		store, err = keychain.Open(path, pass)
		if err != nil {
			// A wrong passphrase costs a moment, so guessing over the
			// socket is no faster than at the terminal; other methods do
			// not wait for it.
			s.mu.Unlock()
			time.Sleep(time.Second)
			s.mu.Lock()
			return nil, failure(-32000, "That passphrase does not open the keychain")
		}
	}
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	table, err := binding.Load(s.options.Paths.Bindings)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	s.options.Keychain, s.options.Bindings = store, table
	return s.keychainStatus(), nil
}

// lockedKeyRefusal says why no task may open yet: the keychain exists but is
// locked, and the model every task talks to needs a key from it. A session
// gets the keychain only when it is built (its tools and egress checks
// depend on it), so a task opened now would stay keyless even after an
// unlock, and fail every model request until it was reopened.
func (s *server) lockedKeyRefusal() string {
	if s.options.Keychain != nil || s.options.Config.Control.Credential == "" {
		return ""
	}
	if _, err := os.Stat(s.options.Paths.Keychain); err != nil {
		return ""
	}
	return "Unlock the keychain first: the model's key is in it, and a task opened while it is locked can't use it"
}

type settingsResult struct {
	ConfigFile string         `json:"config_file"`
	Control    config.Model   `json:"control"`
	Models     []string       `json:"models"`
	Keychain   keychainStatus `json:"keychain"`
	Docs       docsSettings   `json:"docs"`
	// Values are the settings settings.write may change, by dotted name.
	Values map[string]settingValue `json:"values"`
	// Judge says a judge can answer a run's confirms now: jev.content is
	// not "none" and a jev-key is stored in the unlocked keychain.
	Judge bool `json:"judge"`
}

// docsSettings is how documents leave this computer: whether a Google
// credential is configured, and how an upload to Google Docs is handled
// ("ask", "allow" or "deny"; "" until the first upload or Settings sets it).
type docsSettings struct {
	Google       bool   `json:"google"`
	GoogleUpload string `json:"google_upload"`
}
type docsParams struct {
	GoogleUpload string `json:"google_upload"`
}
type controlParams struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Model    string `json:"model"`
	// APIKey, when set, is stored in the keychain and bound to exactly the
	// model's host, POST, as a bearer Authorization header.
	APIKey string `json:"api_key,omitempty"`
}

func (s *server) settings() settingsResult {
	names := make([]string, 0, len(s.options.Config.Models))
	for name := range s.options.Config.Models {
		names = append(names, name)
	}
	// The upload setting is read from config.toml as it is now: a session
	// that asked the question saved the answer there.
	docs := s.options.Config.Docs
	if cfg, err := config.Load(s.options.Paths.ConfigFile); err == nil {
		docs = cfg.Docs
	}
	judge := s.options.Config.Jev.Enabled() && s.options.Keychain != nil && s.options.Keychain.HandleForClass(prim.JevClass) != nil
	return settingsResult{ConfigFile: s.options.Paths.ConfigFile, Control: s.options.Config.Control, Models: names, Keychain: s.keychainStatus(),
		Docs: docsSettings{Google: docs.GoogleCredential != "", GoogleUpload: docs.GoogleUpload}, Values: s.values(), Judge: judge}
}

// docsSettingsMethod answers settings.docs.write: it saves how an upload to
// Google Docs is handled, in config.toml under the lock sessions use when
// they save the same answer.
func (s *server) docsSettingsMethod(req request, _ *peer) (any, *rpcError) {
	var p docsParams
	if err := params(req.Params, &p, "google_upload"); err != nil {
		return nil, err
	}
	switch p.GoogleUpload {
	case config.UploadAsk, config.UploadAllow, config.UploadDeny:
	default:
		return nil, failure(-32602, `google_upload must be "ask", "allow" or "deny"`)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var cfg config.Config
	err := policyfile.Update(ctx, s.options.Paths.ConfigFile, func(raw []byte) ([]byte, error) {
		out, parsed, err := config.SetKey(raw, "docs", "google_upload", p.GoogleUpload)
		cfg = parsed
		return out, err
	})
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	if err := os.Chmod(s.options.Paths.ConfigFile, 0o600); err != nil {
		return nil, failure(-32000, err.Error())
	}
	s.options.Config.Docs.GoogleUpload = cfg.Docs.GoogleUpload
	return s.settings(), nil
}

func (s *server) settingsMethod(req request, _ *peer) (any, *rpcError) {
	if req.Method == "settings.read" {
		if err := params(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		return s.settings(), nil
	}
	var p controlParams
	if err := params(req.Params, &p, "provider", "url", "model", "api_key"); err != nil {
		return nil, err
	}
	key := []byte(p.APIKey)
	defer clear(key)
	if p.Provider != "ollama" && p.Provider != "openai" {
		return nil, failure(-32602, `provider must be "ollama" or "openai"`)
	}
	parsed, err := url.Parse(p.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, failure(-32602, "url must be an http or https address, such as http://localhost:11434")
	}
	if strings.TrimSpace(p.Model) == "" {
		return nil, failure(-32602, "model is required")
	}
	m := config.Model{Provider: p.Provider, URL: strings.TrimRight(p.URL, "/"), Model: strings.TrimSpace(p.Model)}
	// Keep a credential already configured for the same host, unless a new
	// key replaces it.
	// A key goes only over https, except to this computer.
	if (len(key) > 0 || s.options.Config.Control.Credential != "") && parsed.Scheme != "https" && !loopback(parsed.Hostname()) {
		if len(key) > 0 || sameHost(s.options.Config.Control.URL, m.URL) {
			return nil, failure(-32602, "a model with a key must be reached over https")
		}
	}
	if old := s.options.Config.Control; old.Credential != "" && sameHost(old.URL, m.URL) && len(key) == 0 {
		m.Credential = old.Credential
	}
	// Saving the same model on the same server keeps what was tuned by hand
	// in config.toml; choosing another model starts from its defaults.
	if old := s.options.Config.Control; providerName(old.Provider) == providerName(m.Provider) && strings.TrimRight(old.URL, "/") == m.URL && old.Model == m.Model {
		m.NumCtx, m.KeepAlive, m.MaxOutput, m.OutputField = old.NumCtx, old.KeepAlive, old.MaxOutput, old.OutputField
		m.Temperature, m.Seed, m.Vision = old.Temperature, old.Seed, old.Vision
		m.PriceInput, m.PriceCachedInput, m.PriceOutput = old.PriceInput, old.PriceCachedInput, old.PriceOutput
	}
	raw, err := os.ReadFile(s.options.Paths.ConfigFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, failure(-32000, err.Error())
	}
	var class string
	if len(key) > 0 {
		if s.options.Keychain == nil {
			return nil, failure(-32000, "Unlock or create the keychain first: the key is kept there, never in config.toml")
		}
		class = s.freeClass("model-" + hostClass(parsed.Hostname()))
		m.Credential = class
	}
	text, cfg, err := config.SetControl(raw, m)
	if err != nil {
		return nil, failure(-32602, err.Error())
	}
	if len(key) > 0 {
		// The key and its one binding row come first: a config naming a
		// credential that was never stored would fail every request.
		if _, err := s.options.Keychain.Add(class, key); err != nil {
			return nil, failure(-32000, err.Error())
		}
		row, err := binding.Exact(class, parsed.Hostname(), "POST", "header:Authorization:Bearer")
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = binding.AppendExact(ctx, s.options.Paths.Bindings, row)
		cancel()
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		if table, err := binding.Load(s.options.Paths.Bindings); err == nil {
			s.options.Bindings = table
		}
	}
	if err := config.WriteFile(s.options.Paths.ConfigFile, text); err != nil {
		return nil, failure(-32000, err.Error())
	}
	s.options.Config = cfg
	probeCache.mu.Lock()
	probeCache.key = ""
	probeCache.mu.Unlock()
	return s.settings(), nil
}

func sameHost(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	return errA == nil && errB == nil && ua.Host == ub.Host && ua.Scheme == ub.Scheme
}

func loopback(host string) bool { return host == "localhost" || host == "127.0.0.1" || host == "::1" }

var notClass = regexp.MustCompile(`[^a-z0-9]+`)

func hostClass(host string) string {
	c := strings.Trim(notClass.ReplaceAllString(strings.ToLower(host), "-"), "-")
	if c == "" {
		c = "server"
	}
	return c
}

// freeClass returns base, or base-2, base-3…: a keychain lookup by class
// returns the first entry, so a replaced key needs a class of its own.
func (s *server) freeClass(base string) string {
	class := base
	for n := 2; s.options.Keychain.HandleForClass(class) != nil; n++ {
		class = fmt.Sprintf("%s-%d", base, n)
	}
	return class
}

// providerName is a model's provider with the default spelled out.
func providerName(p string) string {
	if p == "" {
		return "ollama"
	}
	return p
}
