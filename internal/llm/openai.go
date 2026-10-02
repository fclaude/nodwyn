package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAI speaks the chat-completions protocol that OpenAI defined and many
// servers copy (OpenRouter, vLLM, LM Studio, llama.cpp). Requests are not
// streamed: output is only shown once whole anyway, because a credential
// can be split across tokens (ADR 25).
type OpenAI struct {
	URL    string // base including the version, e.g. https://api.openai.com/v1
	Client *http.Client
	// Authorize adds the request's credentials, or refuses to send it. It
	// is how a key reaches the request without the provider holding it:
	// the core injects it from the keychain after checking its binding.
	// nil sends no credentials (a local server).
	Authorize func(*http.Request) error
	// MaxTokensField names the request field for the reply cap:
	// "max_tokens" for servers that only know the older field; anything
	// else sends max_completion_tokens.
	MaxTokensField string
	// Images is set when the model takes images (vision = true in its
	// config): a chat-completions server does not say whether one does.
	Images bool
}

// NewOpenAI returns a client for base. Like NewOllama, requests are bounded
// by ctx, not by an overall client timeout, and no redirect is followed.
func NewOpenAI(base string, authorize func(*http.Request) error) *OpenAI {
	return &OpenAI{
		URL: strings.TrimRight(base, "/"),
		Client: &http.Client{
			Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment, IdleConnTimeout: 90 * time.Second},
			CheckRedirect: NoRedirect,
		},
		Authorize: authorize,
	}
}

func (o *OpenAI) Name() string { return "openai" }

// Vision reports what the configuration says: whether the model takes
// images.
func (o *OpenAI) Vision(context.Context, string) (bool, error) { return o.Images, nil }

// Ping lists the server's models.
func (o *OpenAI) Ping(ctx context.Context) (string, error) {
	resp, err := o.send(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	return "openai-compatible", nil
}

// ContextLength asks the server's model list how many tokens model's
// context holds: OpenRouter's context_length (that of the provider it
// routes to first, when it says), or vLLM's max_model_len. The list is
// public on the servers that report it, so the request carries no key.
// 0 and an error when the server does not say.
func (o *OpenAI) ContextLength(ctx context.Context, model string) (int, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL+"/models", nil)
	if err != nil {
		return 0, err
	}
	resp, err := o.Client.Do(hreq)
	if err != nil {
		return 0, fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("openai: model list: %s", resp.Status)
	}
	var list struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			MaxModelLen   int    `json:"max_model_len"`
			TopProvider   struct {
				ContextLength int `json:"context_length"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&list); err != nil {
		return 0, fmt.Errorf("openai: model list: %w", err)
	}
	for _, m := range list.Data {
		if m.ID != model {
			continue
		}
		for _, n := range []int{m.TopProvider.ContextLength, m.ContextLength, m.MaxModelLen} {
			if n > 0 {
				return n, nil
			}
		}
		return 0, fmt.Errorf("openai: the model list does not give %s's context length", model)
	}
	return 0, fmt.Errorf("openai: %s is not in the server's model list", model)
}

// Wire types for /chat/completions.

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    oaiContent    `json:"content"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	// Reasoning is where servers that expose a model's thinking put it;
	// the name varies (DeepSeek and vLLM: reasoning_content).
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// oaiContent is a message's content: a string, or, for a message that
// carries images, an array of parts, its text first and then each image
// as a data URL.
type oaiContent struct {
	Text  string
	Parts []oaiPart
}

type oaiPart struct {
	Type     string `json:"type"` // "text" | "image_url"
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

func (c oaiContent) MarshalJSON() ([]byte, error) {
	if c.Parts != nil {
		return json.Marshal(c.Parts)
	}
	return json.Marshal(c.Text)
}

// UnmarshalJSON reads a reply's content: a string, null (a reply that is
// only tool calls), or parts, whose text is joined.
func (c *oaiContent) UnmarshalJSON(raw []byte) error {
	*c = oaiContent{}
	switch {
	case bytes.Equal(raw, []byte("null")):
		return nil
	case len(raw) > 0 && raw[0] == '[':
		var parts []oaiPart
		if err := json.Unmarshal(raw, &parts); err != nil {
			return err
		}
		for _, p := range parts {
			c.Text += p.Text
		}
		return nil
	}
	return json.Unmarshal(raw, &c.Text)
}

// openAIContent is m's content on the wire: its text, and its images as
// data URLs after it.
func openAIContent(m Message) oaiContent {
	c := oaiContent{Text: m.Content}
	for _, img := range m.Images {
		if len(img.Data) == 0 {
			continue
		}
		if c.Parts == nil {
			c.Parts = []oaiPart{{Type: "text", Text: m.Content}}
		}
		p := oaiPart{Type: "image_url", ImageURL: &struct {
			URL string `json:"url"`
		}{"data:" + img.MediaType + ";base64," + base64.StdEncoding.EncodeToString(img.Data)}}
		c.Parts = append(c.Parts, p)
	}
	return c
}

type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON text, not an object
	} `json:"function"`
}

type oaiRequest struct {
	Model    string       `json:"model"`
	Messages []oaiMessage `json:"messages"`
	Tools    []wireTool   `json:"tools,omitempty"`
	Stream   bool         `json:"stream"`
	// The chat-completions cap on generated tokens, reasoning included;
	// reasoning models refuse the older max_tokens, which some other
	// servers need instead (OpenAI.MaxTokensField). One is set, never both.
	MaxCompletionTokens int      `json:"max_completion_tokens,omitempty"`
	MaxTokens           int      `json:"max_tokens,omitempty"`
	Temperature         *float64 `json:"temperature,omitempty"`
	Seed                *int64   `json:"seed,omitempty"`
}

type oaiResponse struct {
	Choices []struct {
		Message      oaiMessage `json:"message"`
		FinishReason string     `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
}

// usage reads a chat-completions usage object field by field, so a field
// of an unexpected shape costs only that field, never the response: the
// token counts, and when the server reports them, the prompt tokens read
// from and written to its cache, the reasoning tokens, and OpenRouter's
// cost in USD.
func usage(raw json.RawMessage) Usage {
	var fields struct {
		PromptTokens        json.RawMessage `json:"prompt_tokens"`
		CompletionTokens    json.RawMessage `json:"completion_tokens"`
		Cost                json.RawMessage `json:"cost"`
		PromptTokensDetails struct {
			CachedTokens     json.RawMessage `json:"cached_tokens"`
			CacheWriteTokens json.RawMessage `json:"cache_write_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens json.RawMessage `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
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
	var gotPrompt, gotOutput bool
	u.PromptTokens, gotPrompt = count(fields.PromptTokens)
	u.OutputTokens, gotOutput = count(fields.CompletionTokens)
	u.Unreported = !gotPrompt && !gotOutput
	if n, ok := count(fields.PromptTokensDetails.CachedTokens); ok {
		u.CachedTokens = &n
	}
	u.CacheWriteTokens, _ = count(fields.PromptTokensDetails.CacheWriteTokens)
	u.ReasoningTokens, _ = count(fields.CompletionTokensDetails.ReasoningTokens)
	var cost float64
	if len(fields.Cost) > 0 && json.Unmarshal(fields.Cost, &cost) == nil && cost >= 0 {
		u.Cost = &cost
	}
	return u
}

func toOpenAI(req Request) oaiRequest {
	w := oaiRequest{Model: req.Model, MaxCompletionTokens: req.MaxTokens, Temperature: req.Temperature, Seed: req.Seed}
	for _, m := range req.Messages {
		om := oaiMessage{Role: m.Role, Content: openAIContent(m), ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			var ot oaiToolCall
			ot.ID, ot.Type = tc.ID, "function"
			ot.Function.Name = tc.Name
			ot.Function.Arguments = string(tc.Args)
			if ot.Function.Arguments == "" {
				ot.Function.Arguments = "{}"
			}
			om.ToolCalls = append(om.ToolCalls, ot)
		}
		w.Messages = append(w.Messages, om)
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

// Chat sends one chat completion. onDelta is called once with the whole
// output, since the request is not streamed.
func (o *OpenAI) Chat(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error) {
	wire := toOpenAI(req)
	if o.MaxTokensField == "max_tokens" {
		wire.MaxTokens, wire.MaxCompletionTokens = wire.MaxCompletionTokens, 0
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	resp, err := o.send(ctx, http.MethodPost, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var wr oaiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&wr); err != nil {
		return nil, fmt.Errorf("openai: bad response: %w", err)
	}
	if len(wr.Choices) == 0 {
		return nil, fmt.Errorf("openai: response has no choices")
	}
	choice := wr.Choices[0]
	out := &Response{
		Message: Message{
			Role:     RoleAssistant,
			Content:  choice.Message.Content.Text,
			Thinking: choice.Message.Reasoning + choice.Message.ReasoningContent,
		},
		DoneReason: choice.FinishReason,
		Usage:      usage(wr.Usage),
	}
	for i, tc := range choice.Message.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if !json.Valid(args) {
			// Keep what the model sent; the tool reports the bad arguments.
			args, _ = json.Marshal(tc.Function.Arguments)
		}
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{ID: id, Name: tc.Function.Name, Args: args})
	}
	if onDelta != nil && (out.Message.Content != "" || out.Message.Thinking != "") {
		onDelta(Delta{Text: out.Message.Content, Thinking: out.Message.Thinking})
	}
	return out, nil
}

// send makes one authorized request and returns a 200 response. A redirect
// is an error (NoRedirect), and a permanent one: asking again gets the same
// answer.
func (o *OpenAI) send(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	hreq, err := http.NewRequestWithContext(ctx, method, o.URL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		hreq.Header.Set("Content-Type", "application/json")
	}
	if o.Authorize != nil {
		if err := o.Authorize(hreq); err != nil {
			return nil, fmt.Errorf("openai: %w", err)
		}
	}
	resp, err := o.Client.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	if err := Redirected(resp, "model requests"); err != nil {
		resp.Body.Close()
		return nil, Permanent(fmt.Errorf("openai: %w", err))
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("openai: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}
