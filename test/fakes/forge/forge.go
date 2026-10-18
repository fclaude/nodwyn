// Package forge is a fake of the two pull-request listings a workflow polls
// (ADR 53): Gitea's GET /api/v1/repos/OWNER/NAME/pulls, which takes
// "Authorization: token KEY", and GitHub's GET /repos/OWNER/NAME/pulls,
// which takes "Authorization: Bearer KEY". It answers with the open pull
// requests set for a repository, in the fields both forges send, and
// refuses a wrong key as they do. It also takes a review posted to a pull
// request (Gitea's POST .../pulls/N/reviews, GitHub's the same path), which
// it records, for the outbox.
package forge

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Pull is one open pull request.
type Pull struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Author string   `json:"author"`
	Base   string   `json:"base"`
	Head   string   `json:"head"` // the head commit
	Draft  bool     `json:"draft"`
	Labels []string `json:"labels"`
}

// Review is a review the fake was sent.
type Review struct {
	Repo   string
	Number string
	Auth   string
	Body   map[string]any
}

// Forge is the fake: URL is its address.
type Forge struct {
	URL     string
	server  *httptest.Server
	key     string
	mu      sync.Mutex
	pulls   map[string][]Pull
	polls   int
	reviews []Review
}

// New starts a fake that takes key.
func New(key string) *Forge {
	f := &Forge{key: key, pulls: map[string][]Pull{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	f.URL = f.server.URL
	return f
}

func (f *Forge) Close() { f.server.Close() }

// Set makes repo's open pull requests pulls.
func (f *Forge) Set(repo string, pulls ...Pull) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls[repo] = pulls
}

// Polls is how many listings were answered.
func (f *Forge) Polls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls
}

// Reviews are the reviews posted so far.
func (f *Forge) Reviews() []Review {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Review(nil), f.reviews...)
}

func (f *Forge) serve(w http.ResponseWriter, r *http.Request) {
	// PUT /_fake/pulls/OWNER/NAME with a JSON array of Pull sets a
	// repository's open pull requests, for a program that cannot call Set
	// (the browser demo); GET /_fake/reviews lists the reviews posted.
	if rest, ok := strings.CutPrefix(r.URL.Path, "/_fake/pulls/"); ok && r.Method == http.MethodPut {
		var pulls []Pull
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&pulls); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.Set(rest, pulls...)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.URL.Path == "/_fake/reviews" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.Reviews())
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	want := "Bearer " + f.key
	if path != r.URL.Path {
		want = "token " + f.key
	}
	if r.Header.Get("Authorization") != want {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 4 || parts[0] != "repos" || parts[3] != "pulls" {
		http.NotFound(w, r)
		return
	}
	repo := parts[1] + "/" + parts[2]
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && len(parts) == 4:
		f.polls++
		var out []map[string]any
		for _, p := range f.pulls[repo] {
			labels := []map[string]any{}
			for _, l := range p.Labels {
				labels = append(labels, map[string]any{"name": l})
			}
			out = append(out, map[string]any{
				"number": p.Number, "title": p.Title, "body": p.Body, "draft": p.Draft,
				"html_url": f.URL + "/" + repo + "/pulls/" + itoa(p.Number),
				"user":     map[string]any{"login": p.Author}, "base": map[string]any{"ref": p.Base},
				"head": map[string]any{"sha": p.Head, "ref": "branch-" + itoa(p.Number)}, "labels": labels,
			})
		}
		if out == nil {
			out = []map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && len(parts) == 6 && parts[5] == "reviews":
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil {
			http.Error(w, `{"message":"Problems parsing JSON"}`, http.StatusBadRequest)
			return
		}
		f.reviews = append(f.reviews, Review{Repo: repo, Number: parts[4], Auth: r.Header.Get("Authorization"), Body: body})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": len(f.reviews), "state": body["event"]})
	default:
		http.NotFound(w, r)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
