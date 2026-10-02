package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// httpTransport is MCP's streamable HTTP transport: each message is a POST
// to the endpoint, answered as JSON or as server-sent events.
type httpTransport struct {
	endpoint  string
	http      *http.Client
	authorize func(*http.Request) error

	mu      sync.Mutex
	session string
}

// New prepares a client for a remote server at endpoint. authorize, when
// set, runs on every request just before it is sent (binding check and
// credential injection).
func New(endpoint string, authorize func(*http.Request) error) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("mcp: %q is not an http(s) address", endpoint)
	}
	return &Client{t: &httpTransport{
		endpoint:  endpoint,
		authorize: authorize,
		http:      &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}, CheckRedirect: noRedirect},
	}}, nil
}

// noRedirect keeps a request at the host its binding names: a redirect
// would carry arguments (and a credential) somewhere else.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func (t *httpTransport) host() string {
	u, _ := url.Parse(t.endpoint)
	return strings.ToLower(u.Hostname())
}

func (t *httpTransport) close() error { return nil }

func (t *httpTransport) request(ctx context.Context, method string, id int64, body []byte) (*response, error) {
	resp, err := t.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("mcp: %s: %s %s", method, resp.Status, strings.TrimSpace(string(detail)))
	}
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		t.mu.Lock()
		t.session = s
		t.mu.Unlock()
	}
	want := fmt.Sprint(id)
	reply := &response{}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		reply, err = readStream(resp.Body, want)
	} else {
		err = json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(reply)
	}
	if err != nil {
		return nil, fmt.Errorf("mcp: %s: %w", method, err)
	}
	return reply, nil
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

func (t *httpTransport) notify(ctx context.Context, method string, body []byte) error {
	resp, err := t.post(ctx, body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("mcp: %s: %s", method, resp.Status)
	}
	return nil
}

func (t *httpTransport) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	t.mu.Lock()
	if t.session != "" {
		req.Header.Set("Mcp-Session-Id", t.session)
	}
	t.mu.Unlock()
	if t.authorize != nil {
		if err := t.authorize(req); err != nil {
			return nil, err
		}
	}
	return t.http.Do(req)
}
