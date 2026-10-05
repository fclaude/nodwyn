package llm

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The Responses API (POST /responses) is the protocol OpenAI defined after
// chat completions, and the only one on which some of its reasoning models
// take an effort and tools together: gpt-6.1-sol refuses reasoning_effort
// beside function tools on /chat/completions and says to use /v1/responses
// (session 20261005T033050Z-a7ed2b66). A server is sent it when its dialect is
// "responses" (ADR 8, amended 2026-10-05); nothing infers it from the address.
//
// Each request carries the whole conversation, as a chat completion does, and
// store is false, so the server keeps nothing to be continued from and nodwyn
// never sends previous_response_id. A reply's reasoning is not sent back: the
// API takes it back only as the encrypted item it came as, which nodwyn does
// not keep (ADR 9, amended 2026-10-02). Its summary, when the server gives
// one, is read as the reply's thinking.

type respRequest struct {
	Model string `json:"model"`
	// Input is the conversation: respMessage, respCall and respOutput items.
	Input  []any      `json:"input"`
	Tools  []respTool `json:"tools,omitempty"`
	Stream bool       `json:"stream"`
	Store  bool       `json:"store"`
	// MaxOutputTokens is the cap on generated tokens, reasoning included;
	// OpenAI.MaxTokensField names a chat-completions field and is not read.
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	// Seed is sent as it was configured: the Responses API has no seed, and
	// a server that refuses it says so rather than nodwyn dropping it.
	Seed           *int64           `json:"seed,omitempty"`
	PromptCacheKey string           `json:"prompt_cache_key,omitempty"`
	Reasoning      *respReasoning   `json:"reasoning,omitempty"`
	CacheControl   *oaiCacheControl `json:"cache_control,omitempty"`
}

type respReasoning struct {
	Effort string `json:"effort"`
}

type respMessage struct {
	Type string `json:"type"` // "message"
	Role string `json:"role"`
	// Content is the message's text, or, for a message that carries images,
	// its text and then each image as a data URL (respPart).
	Content any `json:"content"`
}

type respPart struct {
	Type     string `json:"type"` // "input_text" | "input_image"
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type respCall struct {
	Type      string `json:"type"` // "function_call"
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON text, not an object
}

type respOutput struct {
	Type   string `json:"type"` // "function_call_output"
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type respTool struct {
	Type        string `json:"type"` // "function"
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
	// Strict is false: the API makes a function strict unless told, and
	// strict wants every parameter required, which nodwyn's optional ones
	// are not (ADR 29).
	Strict bool `json:"strict"`
}

type respResponse struct {
	Status            string `json:"status"` // "completed", "incomplete", "failed"
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Output []struct {
		Type string `json:"type"` // "message", "function_call", "reasoning"
		// Content is a message's output_text and refusal parts, or a
		// reasoning item's reasoning_text.
		Content []struct {
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		} `json:"content"`
		Summary []struct {
			Text string `json:"text"`
		} `json:"summary"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"output"`
	Usage json.RawMessage `json:"usage"`
}

// toResponses is req as a Responses request. A tool result is the output of
// the call it answers; an assistant reply is its text, if it has any, and then
// its calls; its thinking stays out (above).
func toResponses(req Request) respRequest {
	w := respRequest{Model: req.Model, MaxOutputTokens: req.MaxTokens, Temperature: req.Temperature, Seed: req.Seed}
	if req.Effort != "" {
		w.Reasoning = &respReasoning{Effort: req.Effort}
	}
	for _, m := range req.Messages {
		if m.Role == RoleTool {
			w.Input = append(w.Input, respOutput{Type: "function_call_output", CallID: m.ToolCallID, Output: m.Content})
			continue
		}
		// A reply that only calls tools is its calls.
		if m.Content != "" || len(m.ToolCalls) == 0 {
			w.Input = append(w.Input, respMessage{Type: "message", Role: m.Role, Content: responsesContent(m)})
		}
		for _, tc := range m.ToolCalls {
			args := string(tc.Args)
			if args == "" {
				args = "{}"
			}
			w.Input = append(w.Input, respCall{Type: "function_call", CallID: tc.ID, Name: tc.Name, Arguments: args})
		}
	}
	for _, t := range req.Tools {
		w.Tools = append(w.Tools, respTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	return w
}

// responsesContent is m's content on the wire: its text, and its images as
// data URLs after it.
func responsesContent(m Message) any {
	var parts []respPart
	for _, img := range m.Images {
		if len(img.Data) == 0 {
			continue
		}
		if parts == nil {
			parts = []respPart{{Type: "input_text", Text: m.Content}}
		}
		parts = append(parts, respPart{Type: "input_image", ImageURL: "data:" + img.MediaType + ";base64," + base64.StdEncoding.EncodeToString(img.Data)})
	}
	if parts != nil {
		return parts
	}
	return m.Content
}

// respond sends req as one Responses request. Its done reason is a chat
// completion's: "length" when the cap cut it short, "tool_calls" when it
// calls tools, else "stop".
func (o *OpenAI) respond(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error) {
	wire := toResponses(req)
	wire.Stream = true
	wire.CacheControl = o.cacheControl()
	if !o.CacheKeyHeaderOnly {
		wire.PromptCacheKey = o.CacheKey
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	resp, err := o.send(ctx, http.MethodPost, "/responses", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	wr, err := readResponses(resp)
	if err != nil {
		return nil, err
	}
	if wr.Status == "failed" {
		reason := "no reason given"
		if wr.Error != nil && wr.Error.Message != "" {
			reason = wr.Error.Message
		}
		return nil, fmt.Errorf("openai: the response failed: %s", reason)
	}
	out := &Response{Message: Message{Role: RoleAssistant}, DoneReason: "stop", Usage: usage(wr.Usage)}
	var thinking []string
	for _, item := range wr.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				out.Message.Content += c.Text + c.Refusal
			}
		case "reasoning":
			for _, s := range item.Summary {
				thinking = append(thinking, s.Text)
			}
			for _, c := range item.Content {
				thinking = append(thinking, c.Text)
			}
		case "function_call":
			args := json.RawMessage(item.Arguments)
			if !json.Valid(args) {
				// Keep what the model sent; the tool reports the bad arguments.
				args, _ = json.Marshal(item.Arguments)
			}
			id := item.CallID
			if id == "" {
				id = fmt.Sprintf("call_%d", len(out.Message.ToolCalls))
			}
			out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{ID: id, Name: item.Name, Args: args})
		}
	}
	out.Message.Thinking = strings.Join(thinking, "\n\n")
	switch {
	case wr.Status == "incomplete" && wr.IncompleteDetails != nil && wr.IncompleteDetails.Reason == "max_output_tokens":
		out.DoneReason = "length"
	case wr.Status == "incomplete" && wr.IncompleteDetails != nil:
		out.DoneReason = wr.IncompleteDetails.Reason
	case len(out.Message.ToolCalls) > 0:
		out.DoneReason = "tool_calls"
	}
	if onDelta != nil && (out.Message.Content != "" || out.Message.Thinking != "") {
		onDelta(Delta{Text: out.Message.Content, Thinking: out.Message.Thinking})
	}
	return out, nil
}

// readResponses buffers a Responses event stream through its terminal event,
// so a credential split across deltas cannot escape redaction (ADR 25).
// Compatible servers that answer JSON are still accepted.
func readResponses(resp *http.Response) (respResponse, error) {
	var out respResponse
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&out); err != nil {
			return out, fmt.Errorf("openai: bad response: %w", err)
		}
		return out, nil
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 32<<20))
	scanner.Buffer(make([]byte, 4096), 32<<20)
	var data []string
	consume := func() (bool, error) {
		if len(data) == 0 {
			return false, nil
		}
		raw := strings.Join(data, "\n")
		data = nil
		if raw == "[DONE]" {
			return false, nil
		}
		var event struct {
			Type     string       `json:"type"`
			Response respResponse `json:"response"`
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return false, errors.New("openai: malformed Responses stream event")
		}
		switch event.Type {
		case "response.completed", "response.incomplete", "response.failed":
			out = event.Response
			if out.Status != strings.TrimPrefix(event.Type, "response.") {
				return false, errors.New("openai: Responses terminal event has the wrong status")
			}
			return true, nil
		case "error":
			return false, errors.New("openai: the Responses stream reported an error")
		}
		return false, nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			done, err := consume()
			if err != nil || done {
				return out, err
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("openai: read Responses stream: %w", err)
	}
	if done, err := consume(); done || err != nil {
		return out, err
	}
	return out, errors.New("openai: Responses stream ended without a terminal response")
}
