package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Anthropic speaks the Messages API (POST /messages), which Claude models
// are served on and which no other server of the OpenAI family copies
// faithfully: a reasoning effort, thinking and a prompt cache are asked for
// in its own fields. Requests are not streamed, for the reason OpenAI's are
// not (ADR 25).
//
// A request carries the whole conversation: the system prompt as the
// top-level system, each tool result as a tool_result block of a user
// message, and every call and result of one assistant turn in a single
// message, as the API requires. A reply's thinking goes back with it as the
// API gave it, each block with its signature and in its place among the
// calls (Message.Blocks), so a model that thought its way to a design does
// not re-derive it at every call: in session 20261005T151325Z-d9519026,
// with none sent back, 14 calls in a row averaged about 19,000 reasoning
// tokens each, working out the same fix again (ADR 9, amended 2026-10-05).
// A request that carries blocks asks the API to drop, not refuse, any whose
// conversation changed since (block_binding, drop_block): the window drops
// those it knows went stale itself.
type Anthropic struct {
	URL    string // base including the version, e.g. https://api.anthropic.com/v1
	Client *http.Client
	// Authorize adds the request's credentials, or refuses to send it: the
	// x-api-key header, injected from the keychain after its binding is
	// checked (OpenAI.Authorize). nil sends none.
	Authorize func(*http.Request) error
	// Images is set when the model takes images (config.Model.TakesImages).
	Images bool
	// CacheControl asks the API to cache the prompt, as the request's
	// top-level cache_control: "ephemeral" (five minutes) or "1h". Claude
	// caches a prompt only when asked; "" asks nothing.
	CacheControl string
}

// AnthropicVersion is the API version every request names. The API has had
// one stable version since 2023; what a model takes beyond it is asked for in
// fields, not by a newer version.
const AnthropicVersion = "2023-06-01"

// anthropicDefaultMaxTokens is the reply cap sent when a request names none:
// the Messages API refuses a request without max_tokens, unlike the servers
// that leave the cap to themselves (Request.MaxTokens).
const anthropicDefaultMaxTokens = 16384

// NewAnthropic returns a client for base. Like NewOpenAI, requests are
// bounded by ctx, not by an overall client timeout, and no redirect is
// followed.
func NewAnthropic(base string, authorize func(*http.Request) error) *Anthropic {
	return &Anthropic{
		URL: strings.TrimRight(base, "/"),
		Client: &http.Client{
			Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment, IdleConnTimeout: 90 * time.Second},
			CheckRedirect: NoRedirect,
		},
		Authorize: authorize,
	}
}

func (a *Anthropic) Name() string { return "anthropic" }

// Server is the address the client asks (ContextReporter).
func (a *Anthropic) Server() string { return a.URL }

// Vision reports what the configuration says: whether the model takes
// images.
func (a *Anthropic) Vision(context.Context, string) (bool, error) { return a.Images, nil }

// Ping lists the server's models, which the API gives only to a key.
func (a *Anthropic) Ping(ctx context.Context) (string, error) {
	resp, err := a.send(ctx, http.MethodGet, "/models?limit=1", nil)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	return "anthropic-messages", nil
}

// modelPages bounds how many pages of the model list are read: at the page
// size asked for, far more than the API has models.
const modelPages = 5

// Models asks the server for the models it lists, with the context window
// it reports for each (max_input_tokens; 0 when it gives none). The request
// carries the key as a chat request does.
func (a *Anthropic) Models(ctx context.Context) ([]ListedModel, error) {
	var models []ListedModel
	after := ""
	for page := 0; page < modelPages; page++ {
		path := "/models?limit=1000"
		if after != "" {
			path += "&after_id=" + url.QueryEscape(after)
		}
		resp, err := a.send(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var list struct {
			Data []struct {
				ID             string `json:"id"`
				MaxInputTokens int    `json:"max_input_tokens"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&list)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("anthropic: model list: %w", err)
		}
		for _, m := range list.Data {
			models = append(models, ListedModel{ID: m.ID, ContextLength: max(m.MaxInputTokens, 0)})
		}
		if !list.HasMore || list.LastID == "" {
			break
		}
		after = list.LastID
	}
	return models, nil
}

// ContextLength asks the server's model list (Models) how many tokens
// model's context holds: 0 and an error when the server does not say.
func (a *Anthropic) ContextLength(ctx context.Context, model string) (int, error) {
	models, err := a.Models(ctx)
	if err != nil {
		return 0, err
	}
	for _, m := range models {
		if m.ID != model {
			continue
		}
		if m.ContextLength > 0 {
			return m.ContextLength, nil
		}
		return 0, fmt.Errorf("anthropic: the model list does not give %s's context length", model)
	}
	return 0, fmt.Errorf("anthropic: %s is not in the server's model list", model)
}

// Wire types for /messages.

type anthRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    string        `json:"system,omitempty"`
	Messages  []anthMessage `json:"messages"`
	Tools     []anthTool    `json:"tools,omitempty"`
	// Temperature is sent as configured; newer models refuse it, and say so.
	Temperature *float64 `json:"temperature,omitempty"`
	// Thinking and OutputConfig carry an effort (effort.go): the effort is a
	// field of output_config, and thinking is asked for beside it as
	// adaptive, with its summary returned, because on some models thinking is
	// off until it is.
	Thinking     *anthThinking     `json:"thinking,omitempty"`
	OutputConfig *anthOutputConfig `json:"output_config,omitempty"`
	CacheControl *oaiCacheControl  `json:"cache_control,omitempty"`
}

type anthThinking struct {
	Type         string       `json:"type"`    // "adaptive"
	Display      string       `json:"display"` // "summarized"
	BlockBinding *anthBinding `json:"block_binding,omitempty"`
}

// anthBinding says what the API does with a thinking block whose
// conversation changed since it was made: "drop_block" drops it and the
// blocks after it, where the default would refuse the request. It needs the
// thinkingBindingBeta header.
type anthBinding struct {
	PrefixMismatchBehavior string `json:"prefix_mismatch_behavior"`
}

// thinkingBindingBeta is the beta a request naming block_binding needs.
const thinkingBindingBeta = "thinking-binding-controls-2026-08-01"

type anthOutputConfig struct {
	Effort string `json:"effort"`
}

type anthMessage struct {
	Role    string      `json:"role"`
	Content []anthBlock `json:"content"`
}

// anthBlock is a content block: text, image, tool_use or tool_result, by
// Type. Only the fields of its type are set.
type anthBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Source    *anthSource     `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	Thinking  string          `json:"-"`
	Signature string          `json:"-"`
	Data      string          `json:"-"`
}

// MarshalJSON writes a thinking block with its thinking and signature
// always there, as the API wants them back even when the thinking is empty,
// and a redacted one with its data; any other block as it is.
func (b anthBlock) MarshalJSON() ([]byte, error) {
	switch b.Type {
	case "thinking":
		return json.Marshal(struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}{b.Type, b.Thinking, b.Signature})
	case "redacted_thinking":
		return json.Marshal(struct {
			Type string `json:"type"`
			Data string `json:"data"`
		}{b.Type, b.Data})
	}
	type plain anthBlock
	return json.Marshal(plain(b))
}

type anthSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type anthResponse struct {
	Content []struct {
		Type      string          `json:"type"` // "text", "thinking", "redacted_thinking", "tool_use"
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		Data      string          `json:"data"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	StopDetail *struct {
		Category    string `json:"category"`
		Explanation string `json:"explanation"`
	} `json:"stop_details"`
	Usage json.RawMessage `json:"usage"`
}

// toolID is id as the API accepts it: letters, digits, underscores and
// hyphens. An id another provider issued, in a conversation that moved
// between models (ADR 40), may hold other characters; a call and its result
// are changed the same way, so they still pair.
var notToolID = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func toolID(id string) string { return notToolID.ReplaceAllString(id, "_") }

// toAnthropic is req as a Messages request. The system messages are the
// top-level system, in order. A tool result is a block of a user message, ahead
// of the text that follows it there, and every run of user messages and tool
// results is one message: the API wants all the results of a turn's calls
// together, and merges consecutive messages of a role itself, so what is sent
// is what it would read. A message with nothing to say is left out, since the
// API refuses an empty text block. A call whose arguments are not a JSON
// object (the model sent text; the tool reports it) is sent with none, since
// input must be an object.
func toAnthropic(req Request) anthRequest {
	w := anthRequest{Model: req.Model, MaxTokens: req.MaxTokens, Temperature: req.Temperature}
	if w.MaxTokens <= 0 {
		w.MaxTokens = anthropicDefaultMaxTokens
	}
	if req.Effort != "" {
		w.Thinking = &anthThinking{Type: "adaptive", Display: "summarized"}
		w.OutputConfig = &anthOutputConfig{Effort: req.Effort}
	}
	var system []string
	replayed := false
	// results counts the tool_result blocks at the start of the last message,
	// when it is a user's: where the next one goes.
	results := 0
	add := func(role string, blocks ...anthBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(w.Messages); n == 0 || w.Messages[n-1].Role != role {
			w.Messages = append(w.Messages, anthMessage{Role: role})
			results = 0
		}
		last := &w.Messages[len(w.Messages)-1]
		for _, b := range blocks {
			if b.Type == "tool_result" {
				last.Content = append(last.Content, anthBlock{})
				copy(last.Content[results+1:], last.Content[results:])
				last.Content[results] = b
				results++
				continue
			}
			last.Content = append(last.Content, b)
		}
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			system = append(system, m.Content)
		case RoleTool:
			add("user", anthBlock{Type: "tool_result", ToolUseID: toolID(m.ToolCallID), Content: m.Content})
		case RoleAssistant:
			var blocks []anthBlock
			// A reply's signed thinking goes first, and each block that
			// came between its calls before the call it came before.
			thought := func(before int) {
				for _, tb := range m.Blocks {
					if tb.Before == before && (tb.Type == "thinking" || tb.Type == "redacted_thinking") && (tb.Signature != "" || tb.Data != "") {
						blocks = append(blocks, anthBlock{Type: tb.Type, Thinking: tb.Thinking, Signature: tb.Signature, Data: tb.Data})
						replayed = true
					}
				}
			}
			thought(0)
			if m.Content != "" {
				blocks = append(blocks, anthBlock{Type: "text", Text: m.Content})
			}
			for i, tc := range m.ToolCalls {
				if i > 0 {
					thought(i)
				}
				input := tc.Args
				var object map[string]json.RawMessage
				if json.Unmarshal(input, &object) != nil || object == nil {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, anthBlock{Type: "tool_use", ID: toolID(tc.ID), Name: tc.Name, Input: input})
			}
			if len(m.ToolCalls) > 0 {
				thought(len(m.ToolCalls))
			}
			add("assistant", blocks...)
		default:
			var blocks []anthBlock
			if m.Content != "" {
				blocks = append(blocks, anthBlock{Type: "text", Text: m.Content})
			}
			for _, img := range m.Images {
				if len(img.Data) > 0 {
					blocks = append(blocks, anthBlock{Type: "image", Source: &anthSource{Type: "base64", MediaType: img.MediaType, Data: base64.StdEncoding.EncodeToString(img.Data)}})
				}
			}
			add("user", blocks...)
		}
	}
	w.System = strings.Join(system, "\n\n")
	if replayed {
		// Blocks go back only to a model that thinks: the request asks for
		// thinking as the API runs it anyway, so it reads them.
		if w.Thinking == nil {
			w.Thinking = &anthThinking{Type: "adaptive", Display: "summarized"}
		}
		w.Thinking.BlockBinding = &anthBinding{PrefixMismatchBehavior: "drop_block"}
	}
	for _, t := range req.Tools {
		w.Tools = append(w.Tools, anthTool{Name: t.Name, Description: t.Description, InputSchema: t.Parameters})
	}
	return w
}

// Chat sends one message request. onDelta is called once with the whole
// output, since the request is not streamed. Its done reason is a chat
// completion's: "length" when the cap cut it short, "tool_calls" when it
// calls tools, "refusal" when the model declined, else "stop".
func (a *Anthropic) Chat(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error) {
	wire := toAnthropic(req)
	wire.CacheControl = cacheAsk(a.CacheControl)
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	beta := ""
	if wire.Thinking != nil && wire.Thinking.BlockBinding != nil {
		beta = thinkingBindingBeta
	}
	resp, err := a.sendBeta(ctx, http.MethodPost, "/messages", body, beta)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var wr anthResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&wr); err != nil {
		return nil, fmt.Errorf("anthropic: bad response: %w", err)
	}
	out := &Response{Message: Message{Role: RoleAssistant}, DoneReason: "stop", Usage: anthropicUsage(wr.Usage)}
	var thinking []string
	for i, b := range wr.Content {
		switch b.Type {
		case "text":
			out.Message.Content += b.Text
		case "thinking":
			// Empty when the summary was not asked for or the model skipped it.
			if b.Thinking != "" {
				thinking = append(thinking, b.Thinking)
			}
			if b.Signature != "" {
				out.Message.Blocks = append(out.Message.Blocks, ThinkingBlock{Type: b.Type, Thinking: b.Thinking, Signature: b.Signature, Before: len(out.Message.ToolCalls)})
			}
		case "redacted_thinking":
			if b.Data != "" {
				out.Message.Blocks = append(out.Message.Blocks, ThinkingBlock{Type: b.Type, Data: b.Data, Before: len(out.Message.ToolCalls)})
			}
		case "tool_use":
			args := b.Input
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			id := b.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{ID: id, Name: b.Name, Args: args})
		}
	}
	out.Message.Thinking = strings.Join(thinking, "\n\n")
	switch {
	case wr.StopReason == "max_tokens":
		out.DoneReason = "length"
	case wr.StopReason == "refusal":
		// A refusal has no text of its own; the turn says why it has none.
		out.DoneReason = "refusal"
		if out.Message.Content == "" {
			out.Message.Content = refusalText(wr.StopDetail)
		}
	case len(out.Message.ToolCalls) > 0:
		out.DoneReason = "tool_calls"
	}
	if onDelta != nil && (out.Message.Content != "" || out.Message.Thinking != "") {
		onDelta(Delta{Text: out.Message.Content, Thinking: out.Message.Thinking})
	}
	return out, nil
}

// refusalText says that the model declined, with the reason the API gives.
func refusalText(d *struct {
	Category    string `json:"category"`
	Explanation string `json:"explanation"`
}) string {
	text := "The model declined to answer this request."
	if d != nil && d.Explanation != "" {
		text += " " + d.Explanation
	} else if d != nil && d.Category != "" {
		text += " (" + d.Category + ")"
	}
	return text
}

// anthropicUsage reads a Messages usage object field by field, as usage
// reads a chat-completions one. input_tokens counts only the prompt after the
// last cache breakpoint, so the prompt is that plus the tokens read from and
// written to the cache; the reasoning tokens are the part of the output spent
// thinking.
func anthropicUsage(raw json.RawMessage) Usage {
	var fields struct {
		InputTokens         json.RawMessage `json:"input_tokens"`
		OutputTokens        json.RawMessage `json:"output_tokens"`
		CacheReadTokens     json.RawMessage `json:"cache_read_input_tokens"`
		CacheCreationTokens json.RawMessage `json:"cache_creation_input_tokens"`
		OutputDetails       struct {
			ThinkingTokens json.RawMessage `json:"thinking_tokens"`
		} `json:"output_tokens_details"`
	}
	u := Usage{Unreported: true}
	if json.Unmarshal(raw, &fields) != nil {
		return u
	}
	count := func(raw json.RawMessage) (int, bool) {
		var n int
		if len(raw) == 0 || json.Unmarshal(raw, &n) != nil || n < 0 {
			return 0, false
		}
		return n, true
	}
	input, gotInput := count(fields.InputTokens)
	var gotOutput bool
	u.OutputTokens, gotOutput = count(fields.OutputTokens)
	u.Unreported = !gotInput && !gotOutput
	read, gotRead := count(fields.CacheReadTokens)
	written, _ := count(fields.CacheCreationTokens)
	u.PromptTokens = input + read + written
	if gotRead {
		u.CachedTokens = &read
	}
	u.CacheWriteTokens = written
	u.ReasoningTokens, _ = count(fields.OutputDetails.ThinkingTokens)
	return u
}

// send makes one authorized request and returns a 200 response, as
// OpenAI.send does: a redirect is a permanent error, and so is a refusal that
// asking again would not change.
func (a *Anthropic) send(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	return a.sendBeta(ctx, method, path, body, "")
}

// sendBeta is send with an anthropic-beta header when beta is not empty.
func (a *Anthropic) sendBeta(ctx context.Context, method, path string, body []byte, beta string) (*http.Response, error) {
	hreq, err := http.NewRequestWithContext(ctx, method, a.URL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("anthropic-version", AnthropicVersion)
	if beta != "" {
		hreq.Header.Set("anthropic-beta", beta)
	}
	if body != nil {
		hreq.Header.Set("Content-Type", "application/json")
	}
	if a.Authorize != nil {
		if err := a.Authorize(hreq); err != nil {
			return nil, fmt.Errorf("anthropic: %w", err)
		}
	}
	resp, err := a.Client.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	if err := Redirected(resp, "model requests"); err != nil {
		resp.Body.Close()
		return nil, Permanent(fmt.Errorf("anthropic: %w", err))
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, refused("anthropic", resp, strings.TrimSpace(string(b)))
	}
	return resp, nil
}
