package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
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

// notesWrite replaces them through the session's guard, on the caller's
// behalf; the outcome is a policy.result event named "notes".
func (s *server) notesWrite(req request, client *peer) (any, *rpcError) {
	var p notesWriteParams
	if err := params(req.Params, &p, "id", "text"); err != nil {
		return nil, err
	}
	owned, ok := s.sessions[p.ID]
	if !ok {
		return nil, failure(-32002, "Session is not open in this daemon")
	}
	if owned.driver.Active() {
		return nil, failure(-32000, "Stop the active run before editing notes")
	}
	operation, err := owned.session.SaveNotes(p.Text, client.actor)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return inspectionStarted{operation}, nil
}

// modelStatus says whether a configured model's server answers. It is a
// reachability check, not a health check: no credential is sent, and an
// authentication error from a keyed server still means it is there.
type modelStatus struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Host     string `json:"host"`
	State    string `json:"state"` // ready | missing (server answers, model not installed) | unreachable | unconfigured
	Detail   string `json:"detail,omitempty"`
}

// probeCache keeps the last answers briefly, so a page that asks on every
// render does not become a stream of requests to the model server.
var probeCache struct {
	mu   sync.Mutex
	at   time.Time
	key  string
	last []modelStatus
}

const probeFresh = 15 * time.Second

func (s *server) modelsStatus(req request, _ *peer) (any, *rpcError) {
	if err := params(req.Params, &struct{}{}); err != nil {
		return nil, err
	}
	cfg := s.options.Config
	entries := []struct {
		name  string
		model config.Model
	}{{config.ControlModel, cfg.Control}}
	names := make([]string, 0, len(cfg.Models))
	for name := range cfg.Models {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		entries = append(entries, struct {
			name  string
			model config.Model
		}{name, cfg.Models[name]})
	}
	raw, _ := json.Marshal(entries)
	key := string(raw)
	probeCache.mu.Lock()
	if probeCache.key == key && time.Since(probeCache.at) < probeFresh {
		last := probeCache.last
		probeCache.mu.Unlock()
		return last, nil
	}
	probeCache.mu.Unlock()
	// The probes run without the lifecycle lock's help: they are short and
	// bounded, and a slow server must not hold every other method.
	s.mu.Unlock()
	result := make([]modelStatus, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Go(func() { result[i] = probe(e.name, e.model) })
	}
	wg.Wait()
	s.mu.Lock()
	probeCache.mu.Lock()
	probeCache.at, probeCache.key, probeCache.last = time.Now(), key, result
	probeCache.mu.Unlock()
	return result, nil
}

var probeClient = &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}

func probe(name string, m config.Model) modelStatus {
	provider := m.Provider
	if provider == "" {
		provider = "ollama"
	}
	status := modelStatus{Name: name, Provider: provider, Model: m.Model}
	if m.URL == "" || m.Model == "" {
		status.State = "unconfigured"
		return status
	}
	base, err := url.Parse(m.URL)
	if err != nil {
		status.State, status.Detail = "unreachable", "invalid URL"
		return status
	}
	status.Host = base.Host
	endpoint := strings.TrimRight(m.URL, "/") + "/models"
	if provider == "ollama" {
		endpoint = strings.TrimRight(m.URL, "/") + "/api/tags"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		status.State, status.Detail = "unreachable", err.Error()
		return status
	}
	response, err := probeClient.Do(request)
	if err != nil {
		status.State, status.Detail = "unreachable", "no answer from "+base.Host
		return status
	}
	defer response.Body.Close()
	status.State = "ready"
	if provider == "ollama" && response.StatusCode == http.StatusOK {
		var tags struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if json.NewDecoder(http.MaxBytesReader(nil, response.Body, 1<<20)).Decode(&tags) == nil {
			found := false
			for _, t := range tags.Models {
				if t.Name == m.Model || strings.TrimSuffix(t.Name, ":latest") == m.Model {
					found = true
				}
			}
			if !found {
				status.State, status.Detail = "missing", m.Model+" is not installed on "+base.Host
			}
		}
	}
	return status
}
