package rpc

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
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
	// NoPassphrase is set when the open keychain has no passphrase: it
	// opens without one, and the file is as safe as its permissions.
	NoPassphrase bool `json:"no_passphrase,omitempty"`
}
type passphraseParams struct {
	Passphrase string `json:"passphrase"`
}
type keychainCreateParams struct {
	Passphrase string `json:"passphrase,omitempty"`
	// NoPassphrase creates a keychain without one (not recommended).
	NoPassphrase bool `json:"no_passphrase,omitempty"`
}
type keychainPassphraseParams struct {
	// Current is the passphrase the keychain has now; one without a
	// passphrase takes none.
	Current    string `json:"current,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	// NoPassphrase removes it instead (not recommended).
	NoPassphrase bool `json:"no_passphrase,omitempty"`
}

// minPassphrase is the shortest passphrase the console accepts.
const minPassphrase = 8

func (s *server) keychainStatus() keychainStatus {
	status := keychainStatus{}
	if _, err := os.Stat(s.options.Paths.Keychain); err == nil {
		status.Exists = true
	}
	if s.options.Keychain != nil {
		status.Unlocked = true
		status.Credentials = s.options.Keychain.Count()
		status.NoPassphrase = !s.options.Keychain.HasPassphrase()
	}
	return status
}

// wrongPassphrase answers a passphrase that does not open the keychain. It
// costs a moment, so guessing over the socket is no faster than at the
// terminal; other methods do not wait for it.
func (s *server) wrongPassphrase(message string) *rpcError {
	s.mu.Unlock()
	time.Sleep(time.Second)
	s.mu.Lock()
	return failure(-32000, message)
}

func (s *server) keychain(req request, _ *peer) (any, *rpcError) {
	switch req.Method {
	case "keychain.status":
		if err := params(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		return s.keychainStatus(), nil
	case "keychain.passphrase":
		return s.keychainPassphrase(req)
	}
	if s.options.Keychain != nil {
		return nil, failure(-32000, "The keychain is already unlocked")
	}
	path := s.options.Paths.Keychain
	var store *keychain.Store
	var err error
	if req.Method == "keychain.create" {
		var p keychainCreateParams
		if err := params(req.Params, &p, "passphrase", "no_passphrase"); err != nil {
			return nil, err
		}
		pass := []byte(p.Passphrase)
		defer clear(pass)
		switch {
		case p.NoPassphrase && len(pass) > 0:
			return nil, failure(-32602, "Give a passphrase or ask for none, not both")
		case !p.NoPassphrase && len(pass) == 0:
			return nil, failure(-32602, "A passphrase is required")
		case !p.NoPassphrase && len(pass) < minPassphrase:
			return nil, failure(-32602, "Use a passphrase of at least 8 characters")
		}
		if _, statErr := os.Stat(path); statErr == nil {
			return nil, failure(-32000, "A keychain already exists; unlock it instead")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, failure(-32000, err.Error())
		}
		store, err = keychain.Create(path, keychain.DefaultKDF, pass)
	} else {
		var p passphraseParams
		if err := params(req.Params, &p, "passphrase"); err != nil {
			return nil, err
		}
		pass := []byte(p.Passphrase)
		defer clear(pass)
		// An empty passphrase opens a keychain that has none.
		store, err = keychain.Open(path, pass)
		if err != nil {
			if _, statErr := os.Stat(path); statErr != nil {
				return nil, failure(-32000, "There is no keychain yet; create one")
			}
			return nil, s.wrongPassphrase("That passphrase does not open the keychain")
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

// keychainPassphrase sets, changes or removes the passphrase of the open
// keychain. A keychain that has one asks for it first: being at the
// console lets a person use the keys, and removing the passphrase would
// let them read every key from the file.
func (s *server) keychainPassphrase(req request) (any, *rpcError) {
	var p keychainPassphraseParams
	if err := params(req.Params, &p, "current", "passphrase", "no_passphrase"); err != nil {
		return nil, err
	}
	current, pass := []byte(p.Current), []byte(p.Passphrase)
	defer clear(current)
	defer clear(pass)
	store := s.options.Keychain
	if store == nil {
		return nil, failure(-32000, "Unlock the keychain first")
	}
	switch {
	case p.NoPassphrase && len(pass) > 0:
		return nil, failure(-32602, "Give a new passphrase or ask for none, not both")
	case !p.NoPassphrase && len(pass) < minPassphrase:
		return nil, failure(-32602, "Use a passphrase of at least 8 characters")
	case p.NoPassphrase && !store.HasPassphrase():
		return nil, failure(-32000, "The keychain has no passphrase")
	}
	if store.HasPassphrase() {
		if len(current) == 0 {
			return nil, failure(-32602, "Give the keychain's current passphrase")
		}
		if _, err := keychain.Open(s.options.Paths.Keychain, current); err != nil {
			return nil, s.wrongPassphrase("That is not the keychain's current passphrase")
		}
	} else if len(current) > 0 {
		return nil, failure(-32602, "The keychain has no passphrase yet; give only the new one")
	}
	if err := store.SetPassphrase(pass); err != nil {
		return nil, failure(-32000, err.Error())
	}
	return s.keychainStatus(), nil
}

// lockedKeyRefusal says why no task may open yet: the keychain exists but is
// locked, and the model every task talks to needs a key from it. A session
// gets the keychain only when it is built (its tools and egress checks
// depend on it), so a task opened now would stay keyless even after an
// unlock, and fail every model request until it was reopened.
func (s *server) lockedKeyRefusal(choice core.ModelChoice) string {
	if s.options.Keychain != nil {
		return ""
	}
	// The model the task is to talk to: the one chosen for it, else the
	// default. A choice that cannot be had is refused by the session.
	m, _, err := core.Choose(s.options.Config, choice)
	if err != nil || m.Credential == "" {
		return ""
	}
	if _, err := os.Stat(s.options.Paths.Keychain); err != nil {
		return ""
	}
	return "Unlock the keychain first: the model's key is in it, and a task opened while it is locked can't use it"
}

type settingsResult struct {
	ConfigFile string       `json:"config_file"`
	Control    config.Model `json:"control"`
	Models     []string     `json:"models"`
	// Servers are the model providers config.toml names, by name, and the
	// addresses models name inline: the main model may be any model one of
	// them offers (settings.control.write with server, models.offered).
	Servers []modelServer `json:"servers"`
	// Choices are the models a task can be started on or moved to (ADR 41):
	// [control], the default, then each [models.NAME], with the efforts each
	// takes. An entry that is the default model again is not listed twice.
	Choices  []modelChoice  `json:"choices"`
	Keychain keychainStatus `json:"keychain"`
	Docs     docsSettings   `json:"docs"`
	// Values are the settings settings.write may change, by dotted name.
	Values map[string]settingValue `json:"values"`
	// Judge says a judge can answer a run's confirms now: jev.content is
	// not "none" and JevKey is ready.
	Judge bool `json:"judge"`
	// JevKey is what the keychain and the binding table say of the judge's
	// key, as a provider's Key says of its own: ready, or needs_key, locked
	// or unbound, with JevKeyDetail saying what to do. Nothing is asked of
	// the judge's service, so ready says the key can be sent, not that the
	// service accepts it.
	JevKey       string `json:"jev_key"`
	JevKeyDetail string `json:"jev_key_detail,omitempty"`
	// JevHost is the host of jev.url: where a jev-key goes. Empty when the
	// judge has no address. Settings shows its judge-key row only when it
	// is known, so the row never points at a panel with no field.
	JevHost string `json:"jev_host"`
	// JevStored says a jev-key is in the keychain, whether or not the judge
	// is enabled: Settings needs it to tell "no key stored" from "the judge
	// is off with a key stored". The key's value is never included.
	JevStored bool `json:"jev_stored"`
	// JevPublished is what the judge's provider publishes about jev.model at
	// jev.url, its prices, which price its requests when [jev] sets none;
	// absent when the catalogue does not describe it.
	JevPublished *publishedModel `json:"jev_published,omitempty"`
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
	// Name chooses a [models.NAME] entry as the default model, whose
	// settings stay in the entry and are the default's (config.Tuned).
	// Without it, Server names a configured server
	// ([servers.NAME]) the model is on, or Provider and URL say where it is,
	// and the server they make is saved under a name of its own, or found
	// among those configured.
	Name     string `json:"name,omitempty"`
	Server   string `json:"server,omitempty"`
	Provider string `json:"provider,omitempty"`
	URL      string `json:"url,omitempty"`
	// Model is the model id as the provider names it; with Name it is left
	// out.
	Model string `json:"model,omitempty"`
	// Effort is the default effort of the model, one it takes, and "" for none:
	// a new task asks it unless another is chosen. Left out, it is the model's
	// own default effort, or the one it already has when only the model's
	// server is saved again.
	Effort *string `json:"effort,omitempty"`
	// APIKey, when set with Provider and URL, is stored in the keychain
	// and bound to exactly the server's host, POST, as a bearer
	// Authorization header. Every model on the server is sent it.
	APIKey string `json:"api_key,omitempty"`
}

// modelServer is a model provider as Settings shows it: a [servers.NAME]
// table, or an address a model names inline, with its key's keychain class,
// never the key.
type modelServer struct {
	// Authentication identifies a ChatGPT plan credential when the keychain is unlocked.
	Authentication string `json:"authentication,omitempty"`
	Dialect        string `json:"dialect,omitempty"`
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	URL            string `json:"url"`
	Credential     string `json:"credential,omitempty"`
	Host           string `json:"host"`
	// Inline marks an address a model gives itself, without a server of its
	// own: Name is then its host, and it is changed in config.toml, or by
	// saving a default model on a provider.
	Inline bool `json:"inline,omitempty"`
	// Key is what the keychain says of its key without asking the server:
	// empty when it has none, ready, or needs_key, locked or unbound
	// (modelStatus.State), with KeyDetail saying what to do.
	Key       string `json:"key,omitempty"`
	KeyDetail string `json:"key_detail,omitempty"`
	// Uses names the models that are on it: "control", and [models.NAME].
	Uses []string `json:"uses"`
}

// modelChoice is a model a person can choose, as the pickers show it.
type modelChoice struct {
	// Name is "control", or a [models.NAME]: what session.open and
	// session.model.set take.
	Name     string `json:"name"`
	Server   string `json:"server,omitempty"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Host     string `json:"host"`
	// Efforts are the efforts the model takes; none when it takes no effort
	// setting. Effort is the one it starts with when none is chosen: empty
	// asks none, which leaves the model to its provider.
	Efforts []string `json:"efforts,omitempty"`
	Effort  string   `json:"effort,omitempty"`
	// Default marks the model new tasks start on.
	Default bool `json:"default,omitempty"`
	// Settings are the model's: its entry's own, and for the default those
	// of the entry it names (config.Tuned).
	Settings modelSettings `json:"settings"`
	// Published is what its provider publishes about it, which a session
	// uses for each of its settings left unset; none when the catalogue does
	// not describe it.
	Published *publishedModel `json:"published,omitempty"`
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
	// A key stored but not allowed for the judge's host is refused by the
	// guard on the judge's first request, so it is not ready.
	jevHost := prim.JevHost(prim.JevOptions{URL: s.options.Config.Jev.URL})
	jevKey, jevKeyDetail := s.keyCheck(prim.JevClass, jevHost, prim.JevPosition)
	judge := s.options.Config.Jev.Enabled() && jevKey == stateReady
	stored := s.options.Keychain != nil && s.options.Keychain.HandleForClass(prim.JevClass) != nil
	return settingsResult{ConfigFile: s.options.Paths.ConfigFile, Control: s.options.Config.Control, Models: names, Servers: s.providers(), Choices: s.choices(), Keychain: s.keychainStatus(),
		Docs: docsSettings{Google: docs.GoogleCredential != "", GoogleUpload: docs.GoogleUpload}, Values: s.values(), Judge: judge,
		JevHost: jevHost, JevStored: stored, JevKey: jevKey, JevKeyDetail: jevKeyDetail, JevPublished: judgePublished(s.options.Config)}
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
	if err := params(req.Params, &p, "name", "server", "provider", "url", "model", "effort", "api_key"); err != nil {
		return nil, err
	}
	key := []byte(p.APIKey)
	defer clear(key)
	model := strings.TrimSpace(p.Model)
	if p.Name == "" && model == "" {
		return nil, failure(-32602, "model is required")
	}
	raw, err := os.ReadFile(s.options.Paths.ConfigFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, failure(-32000, err.Error())
	}
	// The servers are config.toml's as it is now, so a server added there
	// by hand since nodwyn started is found, never written over.
	current, err := config.Parse(raw)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	old := s.options.Config.Control
	var (
		name, host string
		srv        config.Server
		write      bool
		m          config.Model
	)
	if p.Name != "" {
		// A model already in the list is the default as it is there, its
		// settings and efforts included, which its entry keeps.
		if p.Server != "" || p.Provider != "" || p.URL != "" || model != "" || len(key) > 0 {
			return nil, failure(-32602, "Give a model from the list by name, or a server and a model, not both")
		}
		entry, ok := current.Models[p.Name]
		if p.Name == config.ControlModel {
			entry, ok = current.Control, current.Control.Model != ""
		}
		if !ok {
			return nil, failure(-32602, fmt.Sprintf("No model named %q is configured", p.Name))
		}
		m = entry
		// Its settings and efforts are the entry's: a copy here would go stale
		// when the entry changes, and win over it (Config.Tuned,
		// Config.Efforts). [control] keeps only what it declared itself.
		if p.Name != config.ControlModel {
			m = entry.Choice()
		}
	} else {
		var rerr *rpcError
		name, srv, host, write, rerr = controlServer(p, current.Servers, old, len(key) > 0)
		if rerr != nil {
			return nil, rerr
		}
		m = config.Model{Server: name, Model: model}
		// Saving the same model on the same server keeps what was tuned by hand
		// in config.toml; choosing another model starts from its defaults.
		if providerName(old.Provider) == providerName(srv.Provider) && strings.TrimRight(old.URL, "/") == strings.TrimRight(srv.URL, "/") && old.Model == m.Model {
			m.NumCtx, m.KeepAlive, m.MaxOutput, m.OutputField = old.NumCtx, old.KeepAlive, old.MaxOutput, old.OutputField
			m.Temperature, m.Seed, m.Vision, m.CacheKey, m.CacheControl = old.Temperature, old.Seed, old.Vision, old.CacheKey, old.CacheControl
			m.PriceInput, m.PriceCachedInput, m.PriceOutput = old.PriceInput, old.PriceCachedInput, old.PriceOutput
			m.Efforts, m.Effort = old.Efforts, old.Effort
		}
	}
	// The effort asked for is the default's own; left out, it is what the
	// model already has.
	if p.Effort != nil {
		m.Effort = *p.Effort
	}
	var class string
	if len(key) > 0 {
		if s.options.Keychain == nil {
			return nil, failure(-32000, "Unlock or create the keychain first: the key is kept there, never in config.toml")
		}
		class = s.freeClass("model-" + hostClass(host))
		srv.Credential = class
		write = true
	}
	text := raw
	if write {
		if text, _, err = config.SetServer(raw, name, srv); err != nil {
			return nil, failure(-32602, err.Error())
		}
	}
	text, cfg, err := config.SetControl(text, m)
	if err != nil {
		return nil, failure(-32602, err.Error())
	}
	// Choosing another default does not take the one it replaces out of the
	// models a person can choose: it stays in the list, as it was.
	if text, cfg, err = keepPreviousDefault(text, cfg, current.Control); err != nil {
		return nil, failure(-32602, err.Error())
	}
	if len(key) > 0 {
		// The key and its binding rows come first: a config naming a
		// credential that was never stored would fail every request.
		if _, err := s.options.Keychain.Add(class, key); err != nil {
			return nil, failure(-32000, err.Error())
		}
		if err := s.bindKey(class, host, config.KeyPosition(srv.Provider)); err != nil {
			return nil, failure(-32000, err.Error())
		}
	}
	if err := config.WriteFile(s.options.Paths.ConfigFile, text); err != nil {
		return nil, failure(-32000, err.Error())
	}
	// The catalogue is not in config.toml, so the file read back has none.
	cfg.Catalogue = s.options.Config.Catalogue
	s.options.Config = cfg
	forgetProbes()
	return s.settings(), nil
}

// keepPreviousDefault adds the model that was the default to the models of
// config.toml, as it was, unless it is still the default or is listed already:
// a person who chooses another default has not asked to lose the first.
func keepPreviousDefault(text []byte, cfg config.Config, previous config.Model) ([]byte, config.Config, error) {
	if previous.URL == "" && previous.Server == "" || previous.Model == "" || cfg.SameModel(previous, cfg.Control) {
		return text, cfg, nil
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Models)) {
		if listed := cfg.Models[name]; cfg.SameModel(previous, listed) {
			// What the default held itself fills what its entry leaves unset,
			// so a setting made on [control] is not lost with it.
			if merged := listed.WithSettings(previous); !reflect.DeepEqual(merged, listed) {
				return config.SetModel(text, name, merged)
			}
			return text, cfg, nil
		}
	}
	where := previous.Server
	if where == "" {
		// A default that gave its own address, and was moved to a server at
		// that address, is listed on the server, not as a second copy of it.
		for _, n := range slices.Sorted(maps.Keys(cfg.Servers)) {
			if sv := cfg.Servers[n]; providerName(sv.Provider) == providerName(previous.Provider) && strings.TrimRight(sv.URL, "/") == strings.TrimRight(previous.URL, "/") {
				where = n
				previous.Server, previous.Provider, previous.URL, previous.Dialect, previous.Credential = n, "", "", "", ""
				break
			}
		}
	}
	if where == "" {
		if u, err := url.Parse(previous.URL); err == nil {
			where = serverName(u.Hostname())
		}
	}
	name := modelName(where, previous.Model)
	for n := 2; ; n++ {
		if _, taken := cfg.Models[name]; !taken && name != config.ControlModel {
			break
		}
		name = fmt.Sprintf("%s-%d", modelName(where, previous.Model), n)
	}
	return config.SetModel(text, name, previous)
}

// controlServer is the server settings.control.write puts the main model
// on, under its name among servers, and the host of the address p gives
// (none for a server named). write says the server's table must be
// written: it is new, or (keyed) gets a key. old is the main model until
// now.
func controlServer(p controlParams, servers map[string]config.Server, old config.Model, keyed bool) (name string, srv config.Server, host string, write bool, _ *rpcError) {
	if p.Server != "" {
		if p.Provider != "" || p.URL != "" || keyed {
			return "", srv, "", false, failure(-32602, "Give a configured server, or a provider and url, not both")
		}
		srv, ok := servers[p.Server]
		if !ok {
			return "", srv, "", false, failure(-32602, fmt.Sprintf("No server %q is configured", p.Server))
		}
		return p.Server, srv, "", false, nil
	}
	if p.Provider != "ollama" && p.Provider != "openai" && p.Provider != "anthropic" {
		return "", srv, "", false, failure(-32602, `provider must be "ollama", "openai" or "anthropic"`)
	}
	parsed, err := url.Parse(p.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", srv, "", false, failure(-32602, "url must be an http or https address, such as http://localhost:11434")
	}
	host = parsed.Hostname()
	srv = config.Server{Provider: p.Provider, URL: strings.TrimRight(p.URL, "/")}
	found := false
	// Servers are keyed by name, so several can be at this address: the one
	// the main model names is kept, and the address picks only when one is
	// there.
	var at []string
	for _, n := range slices.Sorted(maps.Keys(servers)) {
		if sv := servers[n]; providerName(sv.Provider) == p.Provider && strings.TrimRight(sv.URL, "/") == srv.URL {
			at = append(at, n)
		}
	}
	switch {
	case old.Server != "" && slices.Contains(at, old.Server):
		name, srv, found = old.Server, servers[old.Server], true
	case len(at) == 1:
		name, srv, found = at[0], servers[at[0]], true
	case len(at) > 1:
		return "", srv, "", false, failure(-32602, fmt.Sprintf("%s has several servers (%s): give the name of the one to use", srv.URL, strings.Join(at, ", ")))
	}
	if !found {
		name = serverName(host)
		for n := 2; servers[name] != (config.Server{}); n++ {
			name = fmt.Sprintf("%s-%d", serverName(host), n)
		}
		// Moving an inline model into a server keeps its wire dialect only
		// at the same endpoint; another address may speak a different one.
		if providerName(old.Provider) == p.Provider && strings.TrimRight(old.URL, "/") == srv.URL {
			srv.Dialect = old.Dialect
		}
		// Keep a key the main model already sends to this host, unless a
		// new one replaces it.
		if old.Credential != "" && sameHost(old.URL, srv.URL) {
			srv.Credential = old.Credential
		}
		write = true
	}
	// A key goes only over https, except to this computer.
	if (keyed || (write && srv.Credential != "")) && parsed.Scheme != "https" && !loopback(host) {
		return "", srv, "", false, failure(-32602, "a model with a key must be reached over https")
	}
	return name, srv, host, write, nil
}

// serverName names a new server after its host: openrouter for
// openrouter.ai, openai for api.openai.com, an address by its digits.
func serverName(host string) string {
	if net.ParseIP(host) == nil {
		labels := strings.Split(host, ".")
		if len(labels) > 1 {
			labels = labels[:len(labels)-1]
		}
		if len(labels) > 1 && (labels[0] == "api" || labels[0] == "www") {
			labels = labels[1:]
		}
		host = strings.Join(labels, ".")
	}
	return hostClass(host)
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
