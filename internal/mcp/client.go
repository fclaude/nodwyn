// Package mcp is a client for remote MCP servers over the streamable HTTP
// transport (ADR 22 §4, ADR 23 §1): initialize, list tools, call a tool.
// It runs no server and spawns nothing. Every request passes through an
// authorize hook the host supplies, which checks the binding table and
// injects the credential; the client never sees a secret.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
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

// Annotations are the server's own hints. nibb shows them and never trusts
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

// Client talks to one server.
type Client struct {
	endpoint  string
	http      *http.Client
	authorize func(*http.Request) error

	mu      sync.Mutex
	session string
	next    int64
}

// New prepares a client for endpoint. authorize, when set, runs on every
// request just before it is sent (binding check and credential injection).
func New(endpoint string, authorize func(*http.Request) error) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("mcp: %q is not an http(s) address", endpoint)
	}
	return &Client{
		endpoint:  endpoint,
		authorize: authorize,
		http:      &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}, CheckRedirect: noRedirect},
	}, nil
}

// noRedirect keeps a request at the host its binding names: a redirect
// would carry arguments (and a credential) somewhere else.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Host is the server's hostname, the destination bindings name.
func (c *Client) Host() string {
	u, _ := url.Parse(c.endpoint)
	return strings.ToLower(u.Hostname())
}

// Initialize opens the MCP session.
func (c *Client) Initialize(ctx context.Context) (ServerInfo, error) {
	var result struct {
		ProtocolVersion string     `json:"protocolVersion"`
		ServerInfo      ServerInfo `json:"serverInfo"`
	}
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "nibb", "version": "1"},
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
	c.mu.Lock()
	c.next++
	id := c.next
	c.mu.Unlock()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("mcp: %s: %s %s", method, resp.Status, strings.TrimSpace(string(detail)))
	}
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.mu.Lock()
		c.session = s
		c.mu.Unlock()
	}
	want := fmt.Sprint(id)
	var reply *response
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		reply, err = readStream(resp.Body, want)
	} else {
		var r response
		err = json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&r)
		reply = &r
	}
	if err != nil {
		return fmt.Errorf("mcp: %s: %w", method, err)
	}
	if reply.Error != nil {
		return fmt.Errorf("mcp: %s: %s", method, reply.Error.Message)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(reply.Result, result)
}

// readStream reads server-sent events until the response to want.
func readStream(r io.Reader, want string) (*response, error) {
	scanner := bufio.NewScanner(io.LimitReader(r, maxBody))
	scanner.Buffer(make([]byte, 64<<10), maxBody)
	var data bytes.Buffer
	flush := func() (*response, bool) {
		defer data.Reset()
		if data.Len() == 0 {
			return nil, false
		}
		var r response
		if json.Unmarshal(data.Bytes(), &r) != nil || strings.Trim(string(r.ID), `"`) != want {
			return nil, false // a notification or another request: not ours
		}
		return &r, true
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if r, ok := flush(); ok {
				return r, nil
			}
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			data.WriteByte('\n')
		}
	}
	if r, ok := flush(); ok {
		return r, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("the stream ended without a response")
}

func (c *Client) notify(ctx context.Context, method string) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	resp, err := c.post(ctx, body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("mcp: %s: %s", method, resp.Status)
	}
	return nil
}

func (c *Client) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	c.mu.Lock()
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	c.mu.Unlock()
	if c.authorize != nil {
		if err := c.authorize(req); err != nil {
			return nil, err
		}
	}
	return c.http.Do(req)
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
