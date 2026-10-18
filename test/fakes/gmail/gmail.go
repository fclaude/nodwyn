// Package gmail is a fake of the parts of Gmail's REST API a workflow uses
// (ADR 53): listing the newest messages of a search, reading one in full (a
// multipart message whose plain text is base64url, as Gmail sends it), and
// creating a draft, whose raw message it decodes and records. It takes
// "Authorization: Bearer KEY" and refuses another, as Gmail does, and ignores
// the search: it lists every message it has, newest first.
package gmail

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Message is one message of a mailbox.
type Message struct {
	ID        string   `json:"id"`
	Thread    string   `json:"thread"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	Subject   string   `json:"subject"`
	MessageID string   `json:"message_id"`
	Date      string   `json:"date"`
	Body      string   `json:"body"`
	Labels    []string `json:"labels"`
}

// Draft is a draft the fake was sent, with its raw message decoded.
type Draft struct {
	Auth   string
	Thread string
	Raw    string
}

// Gmail is the fake: URL is its address.
type Gmail struct {
	URL    string
	server *httptest.Server
	key    string
	mu     sync.Mutex
	boxes  []Message // newest first
	forbid bool
	drafts []Draft
	lists  int
}

// New starts a fake that takes key.
func New(key string) *Gmail {
	g := &Gmail{key: key}
	g.server = httptest.NewServer(http.HandlerFunc(g.serve))
	g.URL = g.server.URL
	return g
}

func (g *Gmail) Close() { g.server.Close() }

// Set makes the mailbox messages, newest first.
func (g *Gmail) Set(messages ...Message) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.boxes = messages
}

// Forbid makes every authorized request answer 403, as Gmail does for a
// credential authorized without the scope the call needs.
func (g *Gmail) Forbid(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.forbid = on
}

// Lists is how many listings were answered.
func (g *Gmail) Lists() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lists
}

// Drafts are the drafts saved so far.
func (g *Gmail) Drafts() []Draft {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Draft{}, g.drafts...)
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func (g *Gmail) serve(w http.ResponseWriter, r *http.Request) {
	// PUT /_fake/messages sets the mailbox, GET /_fake/drafts lists the drafts,
	// for a program that cannot call Set and Drafts (the browser demo).
	if r.URL.Path == "/_fake/messages" && r.Method == http.MethodPut {
		var messages []Message
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&messages); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		g.Set(messages...)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.URL.Path == "/_fake/drafts" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(g.Drafts())
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+g.key {
		http.Error(w, `{"error":{"code":401,"message":"Invalid Credentials"}}`, http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me/")
	g.mu.Lock()
	defer g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if g.forbid {
		http.Error(w, `{"error":{"code":403,"message":"Request had insufficient authentication scopes."}}`, http.StatusForbidden)
		return
	}
	switch {
	case path == "messages" && r.Method == http.MethodGet:
		g.lists++
		out := []map[string]string{}
		for _, m := range g.boxes {
			out = append(out, map[string]string{"id": m.ID, "threadId": m.Thread})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": out, "resultSizeEstimate": len(out)})
	case strings.HasPrefix(path, "messages/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(path, "messages/")
		for _, m := range g.boxes {
			if m.ID != id {
				continue
			}
			headers := []map[string]string{{"name": "From", "value": m.From}, {"name": "To", "value": m.To}, {"name": "Subject", "value": m.Subject}, {"name": "Date", "value": m.Date}, {"name": "Message-ID", "value": m.MessageID}}
			// A multipart/alternative message, an HTML part before the plain
			// text one, as senders write them.
			parts := []map[string]any{
				{"mimeType": "text/html", "body": map[string]any{"data": b64("<p>" + m.Body + "</p>")}},
				{"mimeType": "text/plain", "body": map[string]any{"data": b64(m.Body)}},
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": m.ID, "threadId": m.Thread, "labelIds": m.Labels, "snippet": m.Body,
				"payload": map[string]any{"mimeType": "multipart/alternative", "headers": headers, "parts": parts},
			})
			return
		}
		http.NotFound(w, r)
	case path == "drafts" && r.Method == http.MethodPost:
		var body struct {
			Message struct {
				ThreadID string `json:"threadId"`
				Raw      string `json:"raw"`
			} `json:"message"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, `{"error":{"code":400,"message":"bad json"}}`, http.StatusBadRequest)
			return
		}
		raw, err := base64.URLEncoding.DecodeString(body.Message.Raw)
		if err != nil {
			http.Error(w, `{"error":{"code":400,"message":"raw is not base64url"}}`, http.StatusBadRequest)
			return
		}
		g.drafts = append(g.drafts, Draft{Auth: r.Header.Get("Authorization"), Thread: body.Message.ThreadID, Raw: string(raw)})
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "r-" + body.Message.ThreadID, "message": map[string]string{"threadId": body.Message.ThreadID}})
	default:
		http.NotFound(w, r)
	}
}
