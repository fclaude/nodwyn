package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/llm"
)

// The methods in this file serve the task desk (ADR 30): what a home page
// needs across sessions, read from the sources that already exist.

type attentionItem struct {
	ID      string         `json:"id"`
	Pending []sessionEvent `json:"pending"`
}

// attentionList returns the redacted pending prompts of every open session
// that has one: the same descriptors session.subscribe returns, so a home
// page can say what each waiting session asks without subscribing to its
// transcript. They grant no authority; answers still name a live prompt ID.
func (s *server) attentionList(req request, _ *peer) (any, *rpcError) {
	if err := params(req.Params, &struct{}{}); err != nil {
		return nil, err
	}
	result := []attentionItem{}
	for id, owned := range s.sessions {
		pending, err := owned.session.PendingPrompts()
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		if len(pending) == 0 {
			continue
		}
		item := attentionItem{ID: id}
		for _, ev := range pending {
			item.Pending = append(item.Pending, sessionEvent{id, ev.Kind, ev.Seq, ev.Payload})
		}
		result = append(result, item)
	}
	slices.SortFunc(result, func(a, b attentionItem) int { return strings.Compare(a.ID, b.ID) })
	return result, nil
}

type notesParams struct {
	Repo string `json:"repo"`
}
type notesResult struct {
	Text string `json:"text"`
}
type notesWriteParams struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// notesRead returns a repository's project notes: nodwyn's own data, the text
// every session there is shown, so reading needs no session.
func (s *server) notesRead(req request, _ *peer) (any, *rpcError) {
	var p notesParams
	if err := params(req.Params, &p, "repo"); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(p.Repo, "/") {
		return nil, failure(-32602, "repo must be an absolute path")
	}
	text, err := core.ProjectNotes(s.options.Paths.DataDir, p.Repo)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return notesResult{text}, nil
}

// indexStatus describes a project's repository index (ADR 50), nodwyn's own
// derived data, as notes.read reads its notes: no session needed.
// indexForget deletes it; the next search builds it again.
func (s *server) indexStatus(req request, _ *peer) (any, *rpcError) {
	var p notesParams
	if err := params(req.Params, &p, "repo"); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(p.Repo, "/") {
		return nil, failure(-32602, "repo must be an absolute path")
	}
	if req.Method == "index.forget" {
		if err := core.ForgetProjectIndex(s.options.Paths.DataDir, p.Repo); err != nil {
			return nil, failure(-32000, err.Error())
		}
		return struct{}{}, nil
	}
	info, err := core.ProjectIndex(s.options.Paths.DataDir, p.Repo)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return info, nil
}

// notesWrite replaces them through the session's guard, on the caller's
// behalf; the outcome is a policy.result event named "notes".
func (s *server) notesWrite(req request, client *peer) (any, *rpcError) {
	var p notesWriteParams
	if err := params(req.Params, &p, "id", "text"); err != nil {
		return nil, err
	}
	owned, rerr := s.idle(p.ID, "editing notes")
	if rerr != nil {
		return nil, rerr
	}
	operation, err := owned.session.SaveNotes(p.Text, client.actor)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return inspectionStarted{operation}, nil
}

// modelStatus says whether a configured model can be used: its server
// answers, its key is there and allowed for the server, and the server has
// the model. It is a readiness check, not a health check: no credential is
// sent, and an authentication error from a keyed server still means it is
// there. A key that is not stored, or not allowed, is said before the server
// is asked, since that is what to fix.
type modelStatus struct {
	Name   string `json:"name"`
	Server string `json:"server,omitempty"`
	// Provider is the kind of server: ollama, openai or anthropic.
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Host     string `json:"host"`
	// State is ready, missing (the server answers, the model is not installed),
	// unreachable, unconfigured, needs_key (the key is not stored, or the
	// server wants one and none is configured), locked (the key is in a
	// keychain that is locked) or unbound (the key is stored but not allowed
	// for the server's host).
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// The states a model or a provider is in (modelStatus.State).
const (
	stateReady        = "ready"
	stateMissing      = "missing"
	stateUnreachable  = "unreachable"
	stateUnconfigured = "unconfigured"
	stateNeedsKey     = "needs_key"
	stateLocked       = "locked"
	stateUnbound      = "unbound"
)

// serverProbe is what asking a server for its models found. It is kept
// briefly, so a page that asks on every render does not become a stream of
// requests to the model server.
type serverProbe struct {
	at          time.Time
	host        string
	unreachable string // why, when it did not answer
	// auth says it answered 401 or 403, which a server that lists its models
	// only to a key does.
	auth bool
	// tags are the models an Ollama server has installed; nil when it did not
	// say.
	tags []string
}

var probeCache struct {
	mu       sync.Mutex
	byServer map[string]serverProbe
}

const probeFresh = 15 * time.Second

// forgetProbes drops what the probes found, after the configuration changed.
func forgetProbes() {
	probeCache.mu.Lock()
	probeCache.byServer = nil
	probeCache.mu.Unlock()
}

// probeKey names the server a model is on: the same address is asked once,
// whatever names its models. Servers are keyed by name, so two at one address
// (an API key and a ChatGPT plan) are asked separately, each with its own
// credential.
func probeKey(m config.Model) string {
	return m.Server + "\x00" + providerName(m.Provider) + "\x00" + strings.TrimRight(m.URL, "/")
}

// registryEntry is a model config.toml names: [control] as "control", and
// each [models.NAME], with its server's settings filled in.
type registryEntry struct {
	name  string
	model config.Model
}

// registry is the models the daemon's configuration names, [control] first.
// An unconfigured [control] is left out.
func (s *server) registry() []registryEntry {
	cfg := s.options.Config
	var entries []registryEntry
	if c := cfg.Tuned(cfg.Resolve(cfg.Control)); c.URL != "" && c.Model != "" {
		entries = append(entries, registryEntry{config.ControlModel, c})
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Models)) {
		entries = append(entries, registryEntry{name, cfg.Resolve(cfg.Models[name])})
	}
	return entries
}

// modelsStatusParams ask for the check again: Fresh asks every server now,
// instead of taking what the last check found a few seconds ago.
type modelsStatusParams struct {
	Fresh bool `json:"fresh,omitempty"`
}

func (s *server) modelsStatus(req request, _ *peer) (any, *rpcError) {
	var p modelsStatusParams
	if err := params(req.Params, &p, "fresh"); err != nil {
		return nil, err
	}
	if p.Fresh {
		forgetProbes()
	}
	entries := s.registry()
	cfg := s.options.Config
	if cfg.Control.URL == "" || cfg.Control.Model == "" {
		// The unconfigured main model is reported as such, so a page can say
		// that no model is set up.
		entries = append([]registryEntry{{config.ControlModel, cfg.Control}}, entries...)
	}
	probes := s.probeServers(entries)
	result := make([]modelStatus, len(entries))
	for i, e := range entries {
		result[i] = s.status(e.name, e.model, probes[probeKey(e.model)])
	}
	return result, nil
}

// probeServers asks each distinct server of entries for its models, those the
// last answers do not already hold. The probes run without the lifecycle
// lock's help: they are short and bounded, and a slow server must not hold
// every other method.
func (s *server) probeServers(entries []registryEntry) map[string]serverProbe {
	found := map[string]serverProbe{}
	var ask []config.Model
	probeCache.mu.Lock()
	for _, e := range entries {
		key := probeKey(e.model)
		if _, done := found[key]; done || e.model.URL == "" {
			continue
		}
		if p, ok := probeCache.byServer[key]; ok && time.Since(p.at) < probeFresh {
			found[key] = p
		} else {
			found[key] = serverProbe{}
			ask = append(ask, e.model)
		}
	}
	probeCache.mu.Unlock()
	if len(ask) == 0 {
		return found
	}
	s.mu.Unlock()
	asked := make([]serverProbe, len(ask))
	var wg sync.WaitGroup
	for i, m := range ask {
		wg.Go(func() { asked[i] = probe(s.base(), m) })
	}
	wg.Wait()
	s.mu.Lock()
	probeCache.mu.Lock()
	if probeCache.byServer == nil {
		probeCache.byServer = map[string]serverProbe{}
	}
	for i, m := range ask {
		found[probeKey(m)] = asked[i]
		probeCache.byServer[probeKey(m)] = asked[i]
	}
	probeCache.mu.Unlock()
	return found
}

var probeClient = &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}

// probe asks m's server for the models it lists, without a credential.
func probe(parent context.Context, m config.Model) serverProbe {
	found := serverProbe{at: time.Now()}
	base, err := url.Parse(m.URL)
	if err != nil {
		found.unreachable = "invalid URL"
		return found
	}
	found.host = base.Host
	provider := providerName(m.Provider)
	endpoint := strings.TrimRight(m.URL, "/") + "/models"
	if provider == "ollama" {
		endpoint = strings.TrimRight(m.URL, "/") + "/api/tags"
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		found.unreachable = err.Error()
		return found
	}
	if provider == "anthropic" {
		// Asked without a version, the API may answer 400 before it says the
		// key is missing.
		request.Header.Set("anthropic-version", llm.AnthropicVersion)
	}
	response, err := probeClient.Do(request)
	if err != nil {
		found.unreachable = "No answer from " + base.Host
		return found
	}
	defer response.Body.Close()
	found.auth = response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden
	if provider == "ollama" && response.StatusCode == http.StatusOK {
		var tags struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if json.NewDecoder(http.MaxBytesReader(nil, response.Body, 1<<20)).Decode(&tags) == nil {
			found.tags = []string{}
			for _, t := range tags.Models {
				found.tags = append(found.tags, t.Name)
			}
		}
	}
	return found
}

// keyCheck says what can be known about a model's key without asking its
// server: "" when it has none, stateReady when it is stored and allowed for
// host at position (where its provider reads it: config.KeyPosition), and
// otherwise why it cannot be used and what to do.
func (s *server) keyCheck(class, host, position string) (state, detail string) {
	if class == "" {
		return "", ""
	}
	if s.options.Keychain == nil {
		if _, err := os.Stat(s.options.Paths.Keychain); err == nil {
			return stateLocked, "The keychain is locked: unlock it to use this provider's key"
		}
		return stateNeedsKey, "No key is stored yet: add this provider's API key"
	}
	if s.options.Keychain.HandleForClass(class) == nil {
		return stateNeedsKey, "This provider's key is not in the keychain: add its API key"
	}
	if bound, reason := s.bindingTable().Lookup(class, host, http.MethodPost, position); !bound {
		return stateUnbound, fmt.Sprintf("The key is stored but not allowed for %s (%s): allow it in Connections › Credentials", host, reason)
	}
	return stateReady, ""
}

// bindingTable is the binding table as the file has it now: a row added since
// the daemon started counts, as it does for a session.
func (s *server) bindingTable() binding.Table {
	if s.options.Paths.Bindings != "" {
		if table, err := binding.Load(s.options.Paths.Bindings); err == nil {
			return table
		}
	}
	return s.options.Bindings
}

// keyed is how the daemon's own requests to a keyed server (a model test,
// the list of models offered) get their key: as a task's do, but with
// nothing logged, since no session is asking.
func (s *server) keyed() core.Keyed {
	return core.Keyed{Keychain: s.options.Keychain, Locked: "the keychain is locked", Table: s.bindingTable(), Insecure: s.options.Config.Policy.AllowInsecureHTTP}
}

// status is model m's readiness, given what its server's probe found.
func (s *server) status(name string, m config.Model, p serverProbe) modelStatus {
	status := modelStatus{Name: name, Server: m.Server, Provider: providerName(m.Provider), Model: m.Model, Host: p.host}
	if m.URL == "" || m.Model == "" {
		status.State = stateUnconfigured
		return status
	}
	if u, err := url.Parse(m.URL); err == nil {
		status.Host = u.Host
		if state, detail := s.keyCheck(m.Credential, u.Hostname(), config.KeyPosition(m.Provider)); state != "" && state != stateReady {
			status.State, status.Detail = state, detail
			return status
		}
	}
	switch {
	case p.unreachable != "":
		status.State, status.Detail = stateUnreachable, p.unreachable
	case p.auth && m.Credential == "":
		status.State, status.Detail = stateNeedsKey, "The server answers 401: it wants an API key, and none is configured for it"
	case p.tags != nil && !slices.ContainsFunc(p.tags, func(t string) bool { return t == m.Model || strings.TrimSuffix(t, ":latest") == m.Model }):
		status.State, status.Detail = stateMissing, m.Model+" is not installed on "+status.Host
	default:
		status.State = stateReady
	}
	return status
}

type offeredParams struct {
	Server string `json:"server"` // a [servers.NAME] in config.toml
}

// listClient asks for a server's model list, which can be long
// (OpenRouter's lists hundreds of models).
var listClient = &http.Client{Timeout: 10 * time.Second, Transport: probeClient.Transport}

// modelsOffered answers models.offered: the models a configured server
// lists, for choosing one there. A chat-completions server is asked with its
// key, after the binding check a model's requests have (authorizer), since
// OpenAI and Cerebras list their models only to one; a server that wants a
// key and has none says so, and the model's name is typed instead.
func (s *server) modelsOffered(req request, _ *peer) (any, *rpcError) {
	var p offeredParams
	if err := params(req.Params, &p, "server"); err != nil {
		return nil, err
	}
	srv, ok := s.options.Config.Servers[p.Server]
	if !ok {
		return nil, failure(-32602, "No server "+strconv.Quote(p.Server)+" is configured")
	}
	var authorize func(*http.Request) error
	if srv.Credential != "" {
		authorize = s.keyed().Authorize(srv.Credential, config.KeyPosition(srv.Provider))
	}
	// As the probes do, the request runs without the lifecycle lock.
	s.mu.Unlock()
	names, err := offered(s.base(), srv, authorize)
	s.mu.Lock()
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return names, nil
}

func offered(parent context.Context, srv config.Server, authorize func(*http.Request) error) ([]string, error) {
	base := strings.TrimRight(srv.URL, "/")
	host := base
	if u, err := url.Parse(base); err == nil {
		host = u.Host
	}
	if providerName(srv.Provider) != "ollama" {
		return listed(parent, srv, host, authorize)
	}
	request, err := http.NewRequestWithContext(parent, http.MethodGet, base+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	response, err := listClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s does not answer", host)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s did not list its models: %s", host, response.Status)
	}
	var list struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("%s's model list does not read: %v", host, err)
	}
	names := []string{}
	for _, m := range list.Models {
		names = append(names, m.Name)
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// listed is what a chat-completions or an Anthropic server lists, asked as a
// model's requests are (llm.OpenAI.Models, llm.Anthropic.Models), with what to
// do when it refuses.
func listed(parent context.Context, srv config.Server, host string, authorize func(*http.Request) error) ([]string, error) {
	// A key that is not sent (the keychain is locked, the binding refuses it)
	// is said as such, not as a server that does not answer.
	var unsent error
	wrapped := authorize
	if authorize != nil {
		wrapped = func(r *http.Request) error { unsent = authorize(r); return unsent }
	}
	var lister interface {
		Models(context.Context) ([]llm.ListedModel, error)
	} = llm.NewOpenAI(srv.URL, wrapped)
	if providerName(srv.Provider) == "anthropic" {
		lister = llm.NewAnthropic(srv.URL, wrapped)
	}
	ctx, cancel := context.WithTimeout(parent, listClient.Timeout)
	defer cancel()
	models, err := lister.Models(ctx)
	var se *llm.StatusError
	var netErr net.Error
	switch {
	case unsent != nil:
		return nil, fmt.Errorf("%s is not asked for its models: the key is not sent (%v)", host, unsent)
	case errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden) && srv.Credential == "":
		return nil, fmt.Errorf("%s lists its models only to a key: add the provider's key", host)
	case errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden):
		return nil, fmt.Errorf("%s did not accept the key (%d): replace it", host, se.Status)
	case errors.As(err, &se):
		return nil, fmt.Errorf("%s did not list its models: %s", host, se.Text)
	case errors.As(err, &netErr) || ctx.Err() != nil:
		return nil, fmt.Errorf("%s does not answer", host)
	case err != nil:
		return nil, fmt.Errorf("%s's model list does not read: %v", host, err)
	}
	names := []string{}
	for _, m := range models {
		names = append(names, m.ID)
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}
