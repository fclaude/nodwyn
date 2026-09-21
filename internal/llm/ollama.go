package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Ollama speaks the native /api/chat protocol (ADR 8).
type Ollama struct {
	URL    string // base, e.g. http://workshop.example:11434
	Client *http.Client
}

// NewOllama returns a client for base. The HTTP client has no overall
// timeout: requests are bounded by ctx, and a large cold prompt on a busy
// single-slot server can legitimately take minutes (plan §3.4).
func NewOllama(base string) *Ollama {
	return &Ollama{
		URL: strings.TrimRight(base, "/"),
		Client: &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: 0,
				IdleConnTimeout:       90 * time.Second,
			},
		},
	}
}

func (o *Ollama) Name() string { return "ollama" }

// Ping hits /api/version.
func (o *Ollama) Ping(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL+"/api/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := o.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("ollama: %s", resp.Status)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// Wire types for /api/chat.

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	Thinking   string         `json:"thinking,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Index     int             `json:"index,omitempty"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}

type wireRequest struct {
	Model     string         `json:"model"`
	Messages  []wireMessage  `json:"messages"`
	Tools     []wireTool     `json:"tools,omitempty"`
	Stream    bool           `json:"stream"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
}

type wireChunk struct {
	Message            wireMessage `json:"message"`
	Done               bool        `json:"done"`
	DoneReason         string      `json:"done_reason"`
	TotalDuration      int64       `json:"total_duration"`
	LoadDuration       int64       `json:"load_duration"`
	PromptEvalCount    int         `json:"prompt_eval_count"`
	PromptEvalDuration int64       `json:"prompt_eval_duration"`
	EvalCount          int         `json:"eval_count"`
	EvalDuration       int64       `json:"eval_duration"`
	Error              string      `json:"error"`
}

func toWire(req Request) wireRequest {
	w := wireRequest{Model: req.Model, Stream: true, KeepAlive: req.KeepAlive}
	if req.NumCtx > 0 {
		w.Options = map[string]any{"num_ctx": req.NumCtx}
	}
	for _, m := range req.Messages {
		wm := wireMessage{Role: m.Role, Content: m.Content, ToolName: m.ToolName, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			var wt wireToolCall
			wt.ID = tc.ID
			wt.Function.Name = tc.Name
			wt.Function.Arguments = tc.Args
			if len(wt.Function.Arguments) == 0 {
				wt.Function.Arguments = json.RawMessage("{}")
			}
			wm.ToolCalls = append(wm.ToolCalls, wt)
		}
		w.Messages = append(w.Messages, wm)
	}
	for _, t := range req.Tools {
		var wt wireTool
		wt.Type = "function"
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = t.Parameters
		w.Tools = append(w.Tools, wt)
	}
	return w
}

// Chat streams /api/chat.
func (o *Ollama) Chat(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error) {
	body, err := json.Marshal(toWire(req))
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.URL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := o.Client.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}

	out := &Response{Message: Message{Role: RoleAssistant}}
	var content, thinking strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 32<<20)
	done := false
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ch wireChunk
		if err := json.Unmarshal(line, &ch); err != nil {
			return nil, fmt.Errorf("ollama: bad chunk: %w", err)
		}
		if ch.Error != "" {
			return nil, fmt.Errorf("ollama: %s", ch.Error)
		}
		if ch.Message.Content != "" || ch.Message.Thinking != "" {
			content.WriteString(ch.Message.Content)
			thinking.WriteString(ch.Message.Thinking)
			if onDelta != nil {
				onDelta(Delta{Text: ch.Message.Content, Thinking: ch.Message.Thinking})
			}
		}
		for _, wt := range ch.Message.ToolCalls {
			args := wt.Function.Arguments
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{ID: wt.ID, Name: wt.Function.Name, Args: args})
		}
		if ch.Done {
			done = true
			out.DoneReason = ch.DoneReason
			out.Usage = Usage{
				PromptTokens: ch.PromptEvalCount,
				OutputTokens: ch.EvalCount,
				PromptEvalMS: ch.PromptEvalDuration / 1e6,
				EvalMS:       ch.EvalDuration / 1e6,
				TotalMS:      ch.TotalDuration / 1e6,
				LoadMS:       ch.LoadDuration / 1e6,
			}
			break
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ollama: stream: %w", err)
	}
	if !done {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("ollama: stream ended without a final chunk")
	}
	out.Message.Content = content.String()
	out.Message.Thinking = thinking.String()
	// Give ids to tool calls that came without one so results can refer to them.
	for i := range out.Message.ToolCalls {
		if out.Message.ToolCalls[i].ID == "" {
			out.Message.ToolCalls[i].ID = fmt.Sprintf("call_%d", i)
		}
	}
	if out.DoneReason == "" {
		if len(out.Message.ToolCalls) > 0 {
			out.DoneReason = "tool_calls"
		} else {
			out.DoneReason = "stop"
		}
	}
	return out, nil
}
