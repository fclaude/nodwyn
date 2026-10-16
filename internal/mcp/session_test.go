package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// sessionServer is a streamable HTTP server that issues a numbered session on
// initialize and answers 404 to a request that carries any other, as one that
// expired its session does (P2-053).
type sessionServer struct {
	*httptest.Server
	mu      sync.Mutex
	current string
	issued  int
	deleted []string
}

func newSessionServer() *sessionServer {
	s := &sessionServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method == http.MethodDelete {
			s.deleted = append(s.deleted, r.Header.Get("Mcp-Session-Id"))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Method == "initialize" {
			s.issued++
			s.current = "session-" + string(rune('0'+s.issued))
			w.Header().Set("Mcp-Session-Id", s.current)
		} else if got := r.Header.Get("Mcp-Session-Id"); got != s.current {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}
		reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		w.Header().Set("Content-Type", "application/json")
		w.Write(reply)
	}))
	return s
}

// A server that has expired the session answers 404, and the client opens a
// new one and sends the request again; before, every later call failed with
// "404 Not Found" for the rest of the task. Closing the client ends the
// session with a DELETE.
func TestExpiredSessionIsStartedAgain(t *testing.T) {
	server := newSessionServer()
	defer server.Close()
	c, err := New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	server.current = "expired"
	server.mu.Unlock()
	result, err := c.Call(ctx, "search", nil)
	if err != nil || result.Text != "ok" {
		t.Fatalf("a call after the session expired: %+v %v", result, err)
	}
	if server.issued != 2 {
		t.Fatalf("%d sessions were started, want 2", server.issued)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if len(server.deleted) != 1 || server.deleted[0] != "session-2" {
		t.Fatalf("sessions ended by Close: %v", server.deleted)
	}
}

// A 404 that no session id was sent with is an ordinary error, not an expiry:
// nothing starts over.
func TestNotFoundWithoutASessionIsAnError(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	defer gone.Close()
	c, err := New(gone.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Initialize(context.Background()); err == nil {
		t.Fatal("initialize against a 404 succeeded")
	}
}
