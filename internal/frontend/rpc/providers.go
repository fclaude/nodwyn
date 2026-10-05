package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/policyfile"
)

// The methods in this file let a person set up the model providers nodwyn
// talks to, and the models and efforts they can choose between, from the
// console (ADR 41). They act on config.toml as settings.control.write does:
// every other line is kept, the result is validated as the loader would,
// and what a task is opened on afterwards is what was saved. A key goes to
// the keychain with one binding row, never into config.toml, and never back
// out.

type providerParams struct {
	// Name is the [servers.NAME] to change. Left out, a provider is added,
	// named after its host.
	Name     string `json:"name,omitempty"`
	Provider string `json:"provider"`
	URL      string `json:"url"`
	// Dialect is how an OpenAI-compatible server reads the protocol
	// ("compatible", "cerebras", or "responses" for OpenAI's Responses API);
	// never inferred from the address. Anthropic's has none.
	Dialect string `json:"dialect,omitempty"`
	// APIKey is stored in the unlocked keychain and bound to exactly the
	// provider's host, POST and GET, where the provider reads it (a bearer
	// Authorization header; Anthropic's x-api-key header). A provider that has
	// a key already has it replaced in place.
	APIKey string `json:"api_key,omitempty"`
	// Credential selects a stored OpenAI plan credential by id. Its class,
	// never its tokens, is saved on the provider.
	Credential string `json:"credential,omitempty"`
}

type nameParams struct {
	Name string `json:"name"`
}

type modelWriteParams struct {
	// Name is the [models.NAME] to change. Left out, a model is added, named
	// after its provider and its id.
	Name   string `json:"name,omitempty"`
	Server string `json:"server"`
	Model  string `json:"model"`
	// Efforts are the efforts the model takes, in the words its provider
	// accepts; none when it takes no effort setting. Effort is the one it
	// starts with: one of Efforts, or none.
	Efforts []string `json:"efforts,omitempty"`
	Effort  string   `json:"effort,omitempty"`
	// Settings are the model's own settings, all of them: what is left out
	// is unset. Left out altogether, the entry keeps those it has.
	Settings *modelSettings `json:"settings,omitempty"`
}

// modelSettings are a model's own settings, as Settings › Models edits them
// (ADR 41, amended 2026-10-05): each model has its own, and the default model
// has those of its entry. One left out is unset: the server decides, the
// window's defaults apply, or the model is not priced.
type modelSettings struct {
	// NumCtx is how many tokens the model's context holds; it sizes the
	// window. Unset, an OpenAI-compatible server's model list is asked
	// (OpenRouter's says; OpenAI's and Cerebras' do not), and an Ollama model
	// is asked for the window's size.
	NumCtx int `json:"num_ctx,omitempty"`
	// MaxOutput caps each reply, thinking included; unset is the window's
	// output reserve. OutputField is the request field that carries it to a
	// chat-completions server: max_completion_tokens unless "max_tokens".
	MaxOutput   int    `json:"max_output,omitempty"`
	OutputField string `json:"output_field,omitempty"`
	// KeepAlive is how long Ollama keeps the model loaded. Ollama only.
	KeepAlive   string   `json:"keep_alive,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	Seed        *int64   `json:"seed,omitempty"`
	// Vision says whether the model takes images; unset, it is what its
	// provider publishes, else a chat-completions or Anthropic model is sent
	// them (config.Model.TakesImages), since its server does not say. CacheKey
	// and CacheControl are config.Model's: CacheKey for OpenAI-compatible
	// servers only.
	Vision       *bool  `json:"vision,omitempty"`
	CacheKey     string `json:"cache_key,omitempty"`
	CacheControl string `json:"cache_control,omitempty"`
	// The prices, USD per million tokens, that estimate what a request cost
	// when the provider does not report it, as only OpenRouter does.
	PriceInput       *float64 `json:"price_input,omitempty"`
	PriceCachedInput *float64 `json:"price_cached_input,omitempty"`
	PriceOutput      *float64 `json:"price_output,omitempty"`
}

// settingsOf is m's settings as the wire carries them.
func settingsOf(m config.Model) modelSettings {
	return modelSettings{NumCtx: m.NumCtx, MaxOutput: m.MaxOutput, OutputField: m.OutputField, KeepAlive: m.KeepAlive, Temperature: m.Temperature, Seed: m.Seed,
		Vision: m.Vision, CacheKey: m.CacheKey, CacheControl: m.CacheControl, PriceInput: m.PriceInput, PriceCachedInput: m.PriceCachedInput, PriceOutput: m.PriceOutput}
}

// on is m with these settings in place of its own.
func (p modelSettings) on(m config.Model) config.Model {
	m.NumCtx, m.MaxOutput, m.OutputField, m.KeepAlive, m.Temperature, m.Seed = p.NumCtx, p.MaxOutput, p.OutputField, p.KeepAlive, p.Temperature, p.Seed
	m.Vision, m.CacheKey, m.CacheControl = p.Vision, p.CacheKey, p.CacheControl
	m.PriceInput, m.PriceCachedInput, m.PriceOutput = p.PriceInput, p.PriceCachedInput, p.PriceOutput
	return m
}

// providers lists the model providers as Settings shows them: each
// [servers.NAME], then the addresses models give inline, with what the
// keychain says of each one's key and which models are on it.
func (s *server) providers() []modelServer {
	cfg := s.options.Config
	entries := s.registry()
	out := []modelServer{}
	describe := func(m modelServer) modelServer {
		host := m.URL
		if u, err := url.Parse(m.URL); err == nil {
			host = u.Host
			if state, detail := s.keyCheck(m.Credential, u.Hostname(), config.KeyPosition(m.Provider)); state != "" {
				m.Key, m.KeyDetail = state, detail
			}
		}
		if st := s.options.Keychain; st != nil {
			if h := st.HandleForClass(m.Credential); h != nil {
				if info, err := st.Info(h.ID); err == nil && info.Provider == "openai" && info.Kind == keychain.KindOAuth2 {
					m.Authentication = "chatgpt"
				}
			}
		}
		m.Host = host
		if m.Uses == nil {
			m.Uses = []string{}
		}
		return m
	}
	// A model that gives the address of a server config.toml also has as a
	// [servers.NAME] is on that provider, though it is not sent its key.
	at := func(sv config.Server, m config.Model) bool {
		return providerName(sv.Provider) == providerName(m.Provider) && strings.TrimRight(sv.URL, "/") == strings.TrimRight(m.URL, "/")
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Servers)) {
		sv := cfg.Servers[name]
		m := modelServer{Name: name, Provider: providerName(sv.Provider), Dialect: string(sv.Dialect), URL: sv.URL, Credential: sv.Credential}
		for _, e := range entries {
			if e.model.Server == name || (e.model.Server == "" && at(sv, e.model)) {
				m.Uses = append(m.Uses, e.name)
			}
		}
		out = append(out, describe(m))
	}
	inline := map[string]*modelServer{}
	var order []string
	for _, e := range entries {
		if e.model.Server != "" || slices.ContainsFunc(slices.Collect(maps.Values(cfg.Servers)), func(sv config.Server) bool { return at(sv, e.model) }) {
			continue
		}
		key := probeKey(e.model) + "\x00" + e.model.Credential + "\x00" + string(e.model.Dialect)
		if inline[key] == nil {
			u, _ := url.Parse(e.model.URL)
			host := e.model.URL
			if u != nil {
				host = u.Host
			}
			inline[key] = &modelServer{Name: host, Provider: providerName(e.model.Provider), Dialect: string(e.model.Dialect), URL: e.model.URL, Credential: e.model.Credential, Inline: true}
			order = append(order, key)
		}
		inline[key].Uses = append(inline[key].Uses, e.name)
	}
	for _, key := range order {
		out = append(out, describe(*inline[key]))
	}
	return out
}

// choices lists the models a task can be started on or moved to: [control],
// the default, and each [models.NAME] that is not the default again.
func (s *server) choices() []modelChoice {
	cfg := s.options.Config
	out := []modelChoice{}
	var control *config.Model
	for _, e := range s.registry() {
		if e.name == config.ControlModel {
			control = &e.model
		} else if control != nil && cfg.SameModel(e.model, *control) {
			continue
		}
		host := e.model.URL
		if u, err := url.Parse(e.model.URL); err == nil {
			host = u.Host
		}
		out = append(out, modelChoice{Name: e.name, Server: e.model.Server, Provider: providerName(e.model.Provider), Model: e.model.Model, Host: host,
			Efforts: cfg.Efforts(e.model), Effort: e.model.Effort, Default: e.name == config.ControlModel, Settings: settingsOf(e.model), Published: published(cfg, e.model)})
	}
	return out
}

// publishedModel is what a model's provider publishes about it, as the
// catalogue has it (ADR 41, amended 2026-10-05): what a session uses for each
// of the model's own settings left unset.
type publishedModel struct {
	NumCtx int `json:"num_ctx,omitempty"`
	// MaxOutput is the reply cap it gives: the published limit on a reply, at
	// most a quarter of the context (the entry's when it sets one).
	MaxOutput int      `json:"max_output,omitempty"`
	Efforts   []string `json:"efforts,omitempty"`
	Images    bool     `json:"images"`
	// The prices, USD per million tokens. A provider that charges more for a
	// long prompt does so past TierAbove tokens.
	PriceInput       *float64 `json:"price_input,omitempty"`
	PriceCachedInput *float64 `json:"price_cached_input,omitempty"`
	PriceCacheWrite  *float64 `json:"price_cache_write,omitempty"`
	PriceOutput      *float64 `json:"price_output,omitempty"`
	TierAbove        int      `json:"tier_above,omitempty"`
}

// published is what m's provider publishes about it; nil when the catalogue
// does not describe it.
func published(cfg config.Config, m config.Model) *publishedModel {
	p, ok := cfg.Published(m)
	if !ok {
		return nil
	}
	bare := m
	bare.MaxOutput, bare.Vision = 0, nil
	out := &publishedModel{NumCtx: p.Context, MaxOutput: cfg.WithPublished(bare).MaxOutput, Efforts: p.Efforts, Images: p.Images}
	if price := p.Price; price != nil {
		out.PriceInput, out.PriceCachedInput, out.PriceCacheWrite, out.PriceOutput = &price.Input, price.CacheRead, price.CacheWrite, &price.Output
		if len(price.Tiers) > 0 {
			out.TierAbove = price.Tiers[0].Above
		}
	}
	return out
}

// judgePublished is what the judge's provider publishes about it: its
// prices, when the catalogue describes jev.model at jev.url. cfg is a copy,
// so leaving out [jev]'s own prices leaves the daemon's alone.
func judgePublished(cfg config.Config) *publishedModel {
	cfg.Jev.PriceInput, cfg.Jev.PriceOutput = nil, nil
	in, out := cfg.JevPrices()
	if in == nil || out == nil {
		return nil
	}
	return &publishedModel{PriceInput: in, PriceOutput: out}
}

type publishedParams struct {
	Server string `json:"server"` // a [servers.NAME] in config.toml
	Model  string `json:"model"`  // the model's id on it
}

// modelsPublished answers models.published: what a model on a configured
// server would have from its provider, for a form adding it; null when the
// catalogue does not describe it.
func (s *server) modelsPublished(req request, _ *peer) (any, *rpcError) {
	var p publishedParams
	if err := params(req.Params, &p, "server", "model"); err != nil {
		return nil, err
	}
	cfg := s.options.Config
	if _, ok := cfg.Servers[p.Server]; !ok {
		return nil, failure(-32602, "No server "+strconv.Quote(p.Server)+" is configured")
	}
	return published(cfg, config.Model{Server: p.Server, Model: p.Model}), nil
}

// errStop ends a policyfile.Update callback whose refusal is already in the
// failure it will answer with.
var errStop = errors.New("rpc: refused")

// rewriteConfig reads config.toml as it is now, under the lock sessions use
// when they save a setting, lets change turn it into the text to write, and
// writes it. change refuses with a failure of its own, or returns an error
// for -32602, the validation the loader would have made. The daemon then
// takes the providers, models and default from the file; what it holds
// besides is not config.toml's to say.
func (s *server) rewriteConfig(change func(raw []byte, current config.Config) ([]byte, config.Config, *rpcError)) *rpcError {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var next config.Config
	var refusal *rpcError
	err := policyfile.Update(ctx, s.options.Paths.ConfigFile, func(raw []byte) ([]byte, error) {
		current, err := config.Parse(raw)
		if err != nil {
			refusal = failure(-32000, err.Error())
			return nil, errStop
		}
		text, parsed, rerr := change(raw, current)
		if rerr != nil {
			refusal = rerr
			return nil, errStop
		}
		next = parsed
		return text, nil
	})
	if refusal != nil {
		return refusal
	}
	if err != nil {
		return failure(-32000, err.Error())
	}
	if err := os.Chmod(s.options.Paths.ConfigFile, 0o600); err != nil {
		return failure(-32000, err.Error())
	}
	s.options.Config.Servers, s.options.Config.Models, s.options.Config.Control = next.Servers, next.Models, next.Control
	forgetProbes()
	return nil
}

// providerWrite answers settings.provider.write: it adds a provider, or
// changes one's address, dialect or key.
func (s *server) providerWrite(req request, _ *peer) (any, *rpcError) {
	var p providerParams
	if err := params(req.Params, &p, "name", "provider", "url", "dialect", "api_key", "credential"); err != nil {
		return nil, err
	}
	key := []byte(p.APIKey)
	defer clear(key)
	p.URL = strings.TrimRight(strings.TrimSpace(p.URL), "/")
	if p.Provider != "ollama" && p.Provider != "openai" && p.Provider != "anthropic" {
		return nil, failure(-32602, `provider must be "ollama", "openai" or "anthropic"`)
	}
	parsed, err := url.Parse(p.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, failure(-32602, "url must be an http or https address, such as http://localhost:11434")
	}
	if p.Provider == "ollama" && (p.Dialect != "" || len(key) > 0 || p.Credential != "") {
		return nil, failure(-32602, "An Ollama server takes no dialect and no key")
	}
	if p.Provider == "anthropic" && p.Dialect != "" {
		return nil, failure(-32602, "An Anthropic server takes no dialect: it speaks the Messages API")
	}
	var selected keychain.Info
	if p.Credential != "" {
		if len(key) > 0 {
			return nil, failure(-32602, "Choose a ChatGPT credential or an API key, not both")
		}
		if s.options.Keychain == nil {
			return nil, failure(-32000, "Unlock the keychain before choosing a ChatGPT account")
		}
		selected, err = s.options.Keychain.Info(p.Credential)
		if err != nil || selected.Kind != keychain.KindOAuth2 || selected.Provider != "openai" {
			return nil, failure(-32602, "Choose an OpenAI OAuth2 credential for ChatGPT plan authentication")
		}
		if h := s.options.Keychain.HandleForClass(selected.Class); h == nil || h.ID != selected.ID {
			return nil, failure(-32602, "This credential is shadowed by another with the same class")
		}
		if selected.NeedsReauth || !selected.Refreshable {
			return nil, failure(-32602, "Authorize this ChatGPT account again before saving the provider")
		}
	}
	host := parsed.Hostname()
	rerr := s.rewriteConfig(func(raw []byte, current config.Config) ([]byte, config.Config, *rpcError) {
		name := p.Name
		old, exists := current.Servers[name]
		switch {
		case name != "" && !exists:
			return nil, config.Config{}, failure(-32602, fmt.Sprintf("No provider named %q is configured", name))
		case name == "":
			for n, sv := range current.Servers {
				if providerName(sv.Provider) == p.Provider && strings.TrimRight(sv.URL, "/") == p.URL {
					return nil, config.Config{}, failure(-32602, fmt.Sprintf("%s is already configured, as %q: change that one", p.URL, n))
				}
			}
			name = serverName(host)
			for n := 2; current.Servers[name] != (config.Server{}); n++ {
				name = fmt.Sprintf("%s-%d", serverName(host), n)
			}
		}
		srv := config.Server{Provider: p.Provider, URL: p.URL, Dialect: llm.ChatDialect(p.Dialect)}
		// A key is the address's: it follows a change of path or port, and
		// stays out of reach of a new host until one is given for it.
		if exists && p.Provider != "ollama" && sameHost(old.URL, p.URL) {
			srv.Credential = old.Credential
		}
		if selected.ID != "" {
			srv.Credential = selected.Class
		}
		if len(key) == 0 && srv.Credential != "" && s.options.Keychain != nil {
			if h := s.options.Keychain.HandleForClass(srv.Credential); h != nil {
				info, err := s.options.Keychain.Info(h.ID)
				if err != nil {
					return nil, config.Config{}, failure(-32000, err.Error())
				}
				if info.Provider == "openai" && (p.Provider != "openai" || p.URL != "https://api.openai.com/v1" || p.Dialect != "responses") {
					return nil, config.Config{}, failure(-32602, "ChatGPT plan authentication requires https://api.openai.com/v1 and the Responses dialect")
				}
			}
		}
		if (len(key) > 0 || srv.Credential != "") && parsed.Scheme != "https" && !loopback(host) {
			return nil, config.Config{}, failure(-32602, "a model with a key must be reached over https")
		}
		var store *keychain.Store
		if len(key) > 0 {
			if store = s.options.Keychain; store == nil {
				return nil, config.Config{}, failure(-32000, "Unlock or create the keychain first: the key is kept there, never in config.toml")
			}
			h := store.HandleForClass(srv.Credential)
			var oauth bool
			if h != nil {
				info, err := store.Info(h.ID)
				if err != nil {
					return nil, config.Config{}, failure(-32000, err.Error())
				}
				oauth = info.Kind == keychain.KindOAuth2
			}
			if srv.Credential == "" || h == nil || oauth {
				srv.Credential = s.freeClass("model-" + hostClass(host))
			}
		}
		text, cfg, err := config.SetServer(raw, name, srv)
		if err != nil {
			return nil, config.Config{}, failure(-32602, err.Error())
		}
		if len(key) > 0 {
			// The key and its binding row come before the file that names
			// them: a config naming a credential that was never stored would
			// fail every request.
			if h := store.HandleForClass(srv.Credential); h != nil {
				err = store.Replace(h.ID, key)
			} else {
				_, err = store.Add(srv.Credential, key)
			}
			if err != nil {
				return nil, config.Config{}, failure(-32000, err.Error())
			}
			if err := s.bindKey(srv.Credential, host, config.KeyPosition(srv.Provider)); err != nil {
				return nil, config.Config{}, failure(-32000, err.Error())
			}
		}
		if selected.ID != "" {
			if err := s.bindKey(selected.Class, host, config.KeyPosition(srv.Provider)); err != nil {
				return nil, config.Config{}, failure(-32000, err.Error())
			}
		}
		return text, cfg, nil
	})
	if rerr != nil {
		return nil, rerr
	}
	return s.settings(), nil
}

// modelKeyMethods are what a model key is bound for: POST for the model's
// requests, and GET for its server's model list, which OpenAI and Cerebras
// give only to a key.
var modelKeyMethods = []string{http.MethodPost, http.MethodGet}

// bindKey allows class for host as a model key: a row for each of
// modelKeyMethods, at position (config.KeyPosition: where the provider reads
// the key), that the table does not have.
func (s *server) bindKey(class, host, position string) error {
	var rows []binding.Binding
	for _, method := range modelKeyMethods {
		if bound, _ := s.bindingTable().Lookup(class, host, method, position); bound {
			continue
		}
		row, err := binding.Exact(class, host, method, position)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	return s.appendRows(rows)
}

// bindModelLists gives each model key that config.toml names, on a chat-
// completions server or a model of its own, and that the binding table
// already allows to POST to that address's host, the GET row its model list
// needs; a key saved for Anthropic has both rows from the start. Keys saved before
// modelKeyMethods had GET were bound for POST alone, so their servers' lists
// were refused (401, 403); this adds no host, position or credential the
// table did not already allow.
func (s *server) bindModelLists() error {
	keyed := []config.Server{}
	for _, srv := range s.options.Config.Servers {
		keyed = append(keyed, srv)
	}
	for _, e := range s.registry() {
		keyed = append(keyed, config.Server{Provider: e.model.Provider, URL: e.model.URL, Credential: e.model.Credential})
	}
	table := s.bindingTable()
	var rows []binding.Binding
	for _, srv := range keyed {
		u, err := url.Parse(srv.URL)
		if providerName(srv.Provider) != "openai" || srv.Credential == "" || err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if bound, _ := table.Lookup(srv.Credential, host, http.MethodPost, config.KeyPositionBearer); !bound {
			continue
		}
		if bound, _ := table.Lookup(srv.Credential, host, http.MethodGet, config.KeyPositionBearer); bound {
			continue
		}
		row, err := binding.Exact(srv.Credential, host, http.MethodGet, config.KeyPositionBearer)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(rows, func(r binding.Binding) bool { return r.Class == row.Class && r.Dest == row.Dest }) {
			rows = append(rows, row)
		}
	}
	return s.appendRows(rows)
}

// providerRemove answers settings.provider.remove. A provider models are
// still on is refused, with the names of the models: they would be left with
// nowhere to go. Its key stays in the keychain, where Connections removes it.
func (s *server) providerRemove(req request, _ *peer) (any, *rpcError) {
	var p nameParams
	if err := params(req.Params, &p, "name"); err != nil {
		return nil, err
	}
	rerr := s.rewriteConfig(func(raw []byte, current config.Config) ([]byte, config.Config, *rpcError) {
		if _, ok := current.Servers[p.Name]; !ok {
			return nil, config.Config{}, failure(-32602, fmt.Sprintf("No provider named %q is configured", p.Name))
		}
		var uses []string
		if current.Control.Server == p.Name {
			uses = append(uses, "the default model")
		}
		for _, name := range slices.Sorted(maps.Keys(current.Models)) {
			if current.Models[name].Server == p.Name {
				uses = append(uses, name)
			}
		}
		if len(uses) > 0 {
			return nil, config.Config{}, failure(-32000, fmt.Sprintf("%s is still used by %s: remove those models, or move them to another provider, first", p.Name, strings.Join(uses, ", ")))
		}
		text, cfg, err := config.RemoveServer(raw, p.Name)
		if err != nil {
			return nil, config.Config{}, failure(-32602, err.Error())
		}
		return text, cfg, nil
	})
	if rerr != nil {
		return nil, rerr
	}
	return s.settings(), nil
}

var notName = regexp.MustCompile(`[^a-z0-9]+`)

// modelName names a model entry after its provider and its id: openai-gpt-5.
func modelName(server, model string) string {
	return strings.Trim(notName.ReplaceAllString(strings.ToLower(server+"-"+model), "-"), "-")
}

// modelWrite answers settings.model.write: it adds a model to the list a
// person chooses from, or changes the provider, id, efforts, default effort or
// settings of one that is there. Settings left out stay as the entry has them,
// unless it is another model now. The entry of the default model holds its
// settings: [control] keeps only which model it is and its effort, and what it
// held besides moves to the entry, so a change here is what the default gets.
func (s *server) modelWrite(req request, _ *peer) (any, *rpcError) {
	var p modelWriteParams
	if err := params(req.Params, &p, "name", "server", "model", "efforts", "effort", "settings"); err != nil {
		return nil, err
	}
	if err := knownSettings(req.Params); err != nil {
		return nil, err
	}
	p.Model = strings.TrimSpace(p.Model)
	if p.Model == "" {
		return nil, failure(-32602, "model is required")
	}
	rerr := s.rewriteConfig(func(raw []byte, current config.Config) ([]byte, config.Config, *rpcError) {
		if _, ok := current.Servers[p.Server]; !ok {
			return nil, config.Config{}, failure(-32602, fmt.Sprintf("No provider named %q is configured", p.Server))
		}
		name := p.Name
		if name == "" {
			// A model that is in the list already is changed, not listed twice.
			for _, other := range slices.Sorted(maps.Keys(current.Models)) {
				if m := current.Models[other]; m.Server == p.Server && m.Model == p.Model {
					name = other
					break
				}
			}
		}
		if name == "" {
			name = modelName(p.Server, p.Model)
			for n := 2; ; n++ {
				if _, taken := current.Models[name]; !taken && name != config.ControlModel {
					break
				}
				name = fmt.Sprintf("%s-%d", modelName(p.Server, p.Model), n)
			}
		}
		entry := current.Models[name]
		// The entry's settings belong to the model they were made for.
		if entry.Server != p.Server || entry.Model != p.Model {
			entry = config.Model{}
		}
		// The default takes its settings from the first entry for its model in
		// name order (config.Tuned); when that is this one, what [control]
		// held itself moves here.
		model := config.Model{Server: p.Server, Model: p.Model}
		isDefault := current.SameModel(current.Control, model)
		for other, m := range current.Models {
			if other < name && current.SameModel(m, model) {
				isDefault = false
			}
		}
		if isDefault {
			// The default's settings move to its entry: those [control] held
			// itself fill what the entry leaves unset.
			entry = entry.WithSettings(current.Control)
		}
		if p.Settings != nil {
			entry = p.Settings.on(entry)
		}
		entry.Server, entry.Model, entry.Efforts, entry.Effort = p.Server, p.Model, p.Efforts, p.Effort
		text, cfg, err := config.SetModel(raw, name, entry)
		if err == nil && isDefault {
			text, cfg, err = config.SetControl(text, current.Control.Choice())
		}
		if err != nil {
			return nil, config.Config{}, failure(-32602, err.Error())
		}
		return text, cfg, nil
	})
	if rerr != nil {
		return nil, rerr
	}
	return s.settings(), nil
}

// knownSettings refuses a setting settings.model.write does not know, which
// would otherwise be dropped without a word.
func knownSettings(raw json.RawMessage) *rpcError {
	var p struct {
		Settings json.RawMessage `json:"settings"`
	}
	if json.Unmarshal(raw, &p) != nil || len(p.Settings) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(p.Settings))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&modelSettings{}); err != nil {
		return failure(-32602, "settings: "+err.Error())
	}
	return nil
}

// modelRemove answers settings.model.remove. The default model is not removed
// from here: another is chosen first.
func (s *server) modelRemove(req request, _ *peer) (any, *rpcError) {
	var p nameParams
	if err := params(req.Params, &p, "name"); err != nil {
		return nil, err
	}
	if p.Name == config.ControlModel {
		return nil, failure(-32602, "The default model is changed, not removed: choose another default first")
	}
	rerr := s.rewriteConfig(func(raw []byte, current config.Config) ([]byte, config.Config, *rpcError) {
		entry, ok := current.Models[p.Name]
		if !ok {
			return nil, config.Config{}, failure(-32602, fmt.Sprintf("No model named %q is configured", p.Name))
		}
		// The default model's efforts are its entry's: removing the entry
		// would leave the default naming efforts nobody lists.
		if current.SameModel(entry, current.Control) {
			return nil, config.Config{}, failure(-32000, fmt.Sprintf("%s is the default model: choose another default first, then remove it", p.Name))
		}
		text, cfg, err := config.RemoveModel(raw, p.Name)
		if err != nil {
			return nil, config.Config{}, failure(-32602, err.Error())
		}
		return text, cfg, nil
	})
	if rerr != nil {
		return nil, rerr
	}
	return s.settings(), nil
}

type testParams struct {
	// Name is "control" or a [models.NAME]; or Server and Model name a model
	// on a configured provider, as session.model.set does.
	Name   string `json:"name,omitempty"`
	Server string `json:"server,omitempty"`
	Model  string `json:"model,omitempty"`
	// Effort is the effort to try with it, one the model takes.
	Effort string `json:"effort,omitempty"`
}

// testResult is what a one-line request to a model found.
type testResult struct {
	OK bool `json:"ok"`
	// State is ok; or why not: needs_key, locked or unbound before anything
	// was sent; rejected (the server did not accept the key), no_model (it has
	// no such model), refused (it refused the request, as it does an effort
	// the model does not take), limited (a rate limit or no quota),
	// unreachable, or error.
	State  string `json:"state"`
	Detail string `json:"detail"`
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
	// CacheControl is the ask to cache the prompt the request carried, when
	// the model's entry sets one.
	CacheControl string `json:"cache_control,omitempty"`
	Status       int    `json:"status,omitempty"`
	// Ms is how long the answer took.
	Ms int64 `json:"ms"`
}

// modelTest answers models.test: it sends one short request to the model, as
// a task would, with its key checked against its binding, and says what came
// back. It is how a wrong key, a model the provider does not have and an
// effort it refuses are found before a task meets them. It sends the words
// "Reply with the single word ok." and nothing of any task.
func (s *server) modelTest(req request, client *peer) (any, *rpcError) {
	var p testParams
	if err := params(req.Params, &p, "name", "server", "model", "effort"); err != nil {
		return nil, err
	}
	if strings.Contains(client.actor, " via ") {
		return nil, failure(-32602, "A model is tried from the computer nodwyn runs on, not from a paired device")
	}
	m, effort, err := core.Choose(s.options.Config, core.ModelChoice{Name: p.Name, Server: p.Server, Model: p.Model, Effort: p.Effort})
	if err != nil {
		return nil, failure(-32602, err.Error())
	}
	result := testResult{Model: m.Model, Effort: effort}
	if m.URL == "" || m.Model == "" {
		result.State, result.Detail = stateUnconfigured, "No model is configured"
		return result, nil
	}
	u, err := url.Parse(m.URL)
	if err != nil {
		return nil, failure(-32602, "the model's address is not a URL")
	}
	position := config.KeyPosition(m.Provider)
	if state, detail := s.keyCheck(m.Credential, u.Hostname(), position); state != "" && state != stateReady {
		result.State, result.Detail = state, detail
		return result, nil
	}
	var authorize func(*http.Request) error
	if m.Credential != "" {
		authorize = authorizer(s.options.Keychain, s.bindingTable(), m.Credential, position)
	}
	var provider llm.Provider
	switch m.Provider {
	case "openai":
		oai := llm.NewOpenAI(m.URL, authorize)
		oai.Dialect, oai.MaxTokensField = m.Dialect, m.OutputField
		// As a task's request is: a provider that refuses the ask to cache is
		// found here, not by the first task.
		oai.CacheControl = m.CacheControl
		result.CacheControl = m.CacheControl
		provider = oai
	case "anthropic":
		a := llm.NewAnthropic(m.URL, authorize)
		// As a task's request is: a model that cannot take the ask to cache the
		// prompt is found here.
		a.CacheControl = m.CacheControl
		result.CacheControl = m.CacheControl
		provider = a
	default:
		provider = llm.NewOllama(m.URL)
	}
	// The request runs without the lifecycle lock, as the probes do.
	s.mu.Unlock()
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	_, err = provider.Chat(ctx, llm.Request{Model: m.Model, Messages: []llm.Message{{Role: llm.RoleUser, Content: "Reply with the single word ok."}}, MaxTokens: 256, Effort: effort}, nil)
	cancel()
	s.mu.Lock()
	result.Ms = time.Since(started).Milliseconds()
	classify(&result, err, u.Host, m.Provider)
	return result, nil
}

// authorizer is how a request to a keyed model gets its key: the key goes in
// only if class is bound to the request's host for its method at position,
// checked on every request, as a task's is. The daemon holds neither key nor
// binding table beyond the call.
func authorizer(store *keychain.Store, table binding.Table, class, position string) func(*http.Request) error {
	return func(req *http.Request) error {
		if store == nil {
			return llm.Permanent(errors.New("the keychain is locked"))
		}
		h := store.HandleForClass(class)
		if h == nil {
			return llm.Permanent(errors.New("no key in the keychain"))
		}
		info, err := store.Info(h.ID)
		if err != nil {
			return llm.Permanent(err)
		}
		if bound, reason := table.Lookup(class, strings.ToLower(req.URL.Hostname()), req.Method, position, info.Scopes...); !bound {
			return llm.Permanent(errors.New(reason))
		}
		if err := store.Refresh(req.Context(), h.ID, nil); err != nil {
			return llm.Permanent(err)
		}
		_, name, prefix := binding.Position(position)
		return store.InjectHeader(h.ID, name, prefix, req)
	}
}

// classify turns what a model test returned into the words Settings shows.
// provider is the kind of server the model is on: what to try differs when a
// request it refuses was sent to Anthropic.
func classify(r *testResult, err error, host, provider string) {
	if err == nil {
		r.OK, r.State, r.Detail = true, "ok", fmt.Sprintf("%s answered in %.1f s", r.Model, float64(r.Ms)/1000)
		return
	}
	var se *llm.StatusError
	var netErr net.Error
	switch {
	case errors.As(err, &se):
	case errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded):
		r.State, r.Detail = stateUnreachable, "No answer from "+host
		return
	default:
		r.State, r.Detail = "error", err.Error()
		return
	}
	r.Status = se.Status
	said := providerSaid(se.Body)
	switch se.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		r.State, r.Detail = "rejected", fmt.Sprintf("%s did not accept the key (%d). Replace it with one from the provider. It said: %s", host, se.Status, said)
	case http.StatusNotFound:
		r.State, r.Detail = "no_model", fmt.Sprintf("%s has no model %q (%d). It said: %s", host, r.Model, se.Status, said)
	case http.StatusTooManyRequests:
		r.State, r.Detail = "limited", fmt.Sprintf("%s is limiting requests, or the account has no quota left (429). It said: %s", host, said)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		hint := ""
		if r.Effort != "" && provider == "anthropic" {
			hint = fmt.Sprintf(" If the effort %q is the cause: an effort asks for adaptive thinking, which Claude models before 4.6 do not have, and the levels a model takes differ; declare the efforts this model takes, or none.", r.Effort)
		} else if r.Effort != "" {
			hint = fmt.Sprintf(" If the effort %q is the cause, declare the efforts this model takes, or choose its default.", r.Effort)
		}
		if r.CacheControl != "" && provider != "anthropic" {
			hint += fmt.Sprintf(" If asking it to cache the prompt (cache_control %q) is the cause, turn Prompt cache off for this model: only a Claude route takes it.", r.CacheControl)
		}
		r.State, r.Detail = "refused", fmt.Sprintf("%s refused the request (%d).%s It said: %s", host, se.Status, hint, said)
	default:
		r.State, r.Detail = "error", se.Error()
	}
}

// providerSaid is what a provider's refusal says: the message of the error
// object chat-completions servers send ({"error": {"message": …}}, or
// {"error": "…"}), else the body as it came, kept short.
func providerSaid(body string) string {
	var parsed struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(body), &parsed) == nil && len(parsed.Error) > 0 {
		var object struct {
			Message string `json:"message"`
		}
		var text string
		switch {
		case json.Unmarshal(parsed.Error, &object) == nil && object.Message != "":
			return object.Message
		case json.Unmarshal(parsed.Error, &text) == nil && text != "":
			return text
		}
	}
	body = strings.TrimSpace(body)
	if len(body) > 300 {
		body = body[:300] + "…"
	}
	return body
}
