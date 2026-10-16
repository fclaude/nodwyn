// Package mcp is a client for MCP servers (ADR 22 §4, ADR 23): initialize,
// list tools, call a tool. It speaks JSON-RPC over either of MCP's
// transports: streamable HTTP to a remote server (New), or one message per
// line on the standard input and output of a server nodwyn runs in a
// container (NewStdio). It spawns nothing itself. Every HTTP request passes
// through an authorize hook the host supplies, which checks the binding
// table and injects the credential; the client never sees a secret.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// ProtocolVersion is the MCP revision this client speaks.
const ProtocolVersion = "2025-06-18"

// maxBody bounds any one response.
const maxBody = 8 << 20

// Tool is one tool a server offers, as its manifest describes it.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	Annotations *Annotations    `json:"annotations,omitempty"`
}

// Annotations are the server's own hints. nodwyn shows them and never trusts
// them: what a tool may do is the person's declaration, not the server's.
type Annotations struct {
	ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	OpenWorldHint   *bool `json:"openWorldHint,omitempty"`
}

// ServerInfo is what initialize reports.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// transport carries JSON-RPC messages to one server and its answers back.
type transport interface {
	// request sends a request with id and returns the response to it.
	request(ctx context.Context, method string, id int64, body []byte) (*response, error)
	// notify sends a notification, which has no response.
	notify(ctx context.Context, method string, body []byte) error
	host() string
	close() error
}

// Client talks to one server.
type Client struct {
	t    transport
	mu   sync.Mutex
	next int64
	// initMu serializes starting a new session after the server dropped the
	// old one; epoch counts the sessions started that way, so calls that
	// all met the same expiry start one new session, not one each.
	initMu sync.Mutex
	epoch  atomic.Int64
}

// Host is a remote server's hostname, the destination bindings name; ""
// for a server over stdio.
func (c *Client) Host() string { return c.t.host() }

// Close ends the conversation: for a server over stdio, its input closes.
func (c *Client) Close() error { return c.t.close() }

// Initialize opens the MCP session.
func (c *Client) Initialize(ctx context.Context) (ServerInfo, error) {
	var result struct {
		ProtocolVersion string     `json:"protocolVersion"`
		ServerInfo      ServerInfo `json:"serverInfo"`
	}
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "nodwyn", "version": "1"},
	}
	if err := c.call(ctx, "initialize", params, &result); err != nil {
		return ServerInfo{}, err
	}
	if err := c.notify(ctx, "notifications/initialized"); err != nil {
		return ServerInfo{}, err
	}
	return result.ServerInfo, nil
}

// Tools lists every tool, following pagination.
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	for range 50 {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := c.call(ctx, "tools/list", params, &result); err != nil {
			return nil, err
		}
		all = append(all, result.Tools...)
		if result.NextCursor == "" {
			return all, nil
		}
		cursor = result.NextCursor
	}
	return nil, errors.New("mcp: too many pages of tools")
}

// Result is a tool call's outcome, as text.
type Result struct {
	Text    string
	IsError bool
}

// Call applies a tool. Content other than text is described, not decoded.
func (c *Client) Call(ctx context.Context, name string, args map[string]any) (Result, error) {
	var result struct {
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			MimeType string          `json:"mimeType"`
			Resource json.RawMessage `json:"resource"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if args == nil {
		args = map[string]any{}
	}
	if err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &result); err != nil {
		return Result{}, err
	}
	var b strings.Builder
	for _, part := range result.Content {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		switch part.Type {
		case "text":
			b.WriteString(part.Text)
		case "resource":
			b.WriteString(string(part.Resource))
		default:
			fmt.Fprintf(&b, "[%s content (%s) not shown]", part.Type, part.MimeType)
		}
	}
	if b.Len() == 0 && len(result.StructuredContent) > 0 {
		b.Write(result.StructuredContent)
	}
	return Result{Text: b.String(), IsError: result.IsError}, nil
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	epoch := c.epoch.Load()
	reply, err := c.send(ctx, method, params)
	if errors.Is(err, errSessionExpired) {
		// A server drops a session it has expired: the specification has the
		// client initialize again and send the request once more. A task
		// kept open for hours otherwise failed every call to the server.
		if err := c.reinitialize(ctx, epoch); err != nil {
			return err
		}
		reply, err = c.send(ctx, method, params)
	}
	if err != nil {
		return err
	}
	if reply.Error != nil {
		return fmt.Errorf("mcp: %s: %s", method, reply.Error.Message)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(reply.Result, result)
}

// send makes one request and returns its response.
func (c *Client) send(ctx context.Context, method string, params any) (*response, error) {
	c.mu.Lock()
	c.next++
	id := c.next
	c.mu.Unlock()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	return c.t.request(ctx, method, id, body)
}

// reinitialize opens a new session after the server dropped the one a call
// of epoch used, unless another call has already done so.
func (c *Client) reinitialize(ctx context.Context, epoch int64) error {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	if c.epoch.Load() != epoch {
		return nil
	}
	if _, err := c.Initialize(ctx); err != nil {
		return err
	}
	c.epoch.Add(1)
	return nil
}

func (c *Client) notify(ctx context.Context, method string) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	return c.t.notify(ctx, method, body)
}

// Manifest is the pinned identity of a tool list (ADR 23 §2): a hash over
// every name, title, description, input schema and annotation, sorted by
// name, so any change a model could read changes it.
func Manifest(tools []Tool) string {
	sorted := append([]Tool(nil), tools...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	h := sha256.New()
	for _, t := range sorted {
		schema := canonical(t.InputSchema)
		annotations, _ := json.Marshal(t.Annotations)
		fmt.Fprintf(h, "%q\x00%q\x00%q\x00%s\x00%s\x00", t.Name, t.Title, t.Description, schema, annotations)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// canonical re-encodes JSON with sorted keys, so formatting is not identity.
func canonical(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	out, _ := json.Marshal(v) // encoding/json sorts map keys
	return out
}
