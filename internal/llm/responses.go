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
	"slices"
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
// never sends previous_response_id. A reply's reasoning goes back only as the
// encrypted item it came as, and only for a model that keeps it
// (OpenAI.ReasoningOrigin, ADR 9 amended 2026-10-07); otherwise it is not
// kept. Its summary, when the server gives one, is read as the reply's
// thinking.

type respRequest struct {
	Model string `json:"model"`
	// Instructions is the system prompt. A plan-authenticated request is
	// refused a system message in its input ("System messages are not
	// allowed", session 20261007T152356Z-a337f2a9), and instructions are the field the API
	// has for the system prompt, so they go there for every credential.
	Instructions string `json:"instructions,omitempty"`
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
	// Include asks for each reply's reasoning encrypted, for a model that
	// keeps it.
	Include []string `json:"include,omitempty"`
}

type respReasoning struct {
	Effort string `json:"effort,omitempty"`
	// Context is "all_turns" for a model that keeps its reasoning: earlier
	// turns' stays in what the model reads, so what a turn's requests sent
	// is still the start of the next turn's and the cache holds.
	Context string `json:"context,omitempty"`
}

// respReasoningItem is a reply's reasoning given back as it came: the
// encrypted content is what the server reads.
type respReasoningItem struct {
	Type             string        `json:"type"` // "reasoning"
	Summary          []respSummary `json:"summary"`
	EncryptedContent string        `json:"encrypted_content"`
}

type respSummary struct {
	Type string `json:"type"` // "summary_text"
	Text string `json:"text"`
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
	Output []respItem      `json:"output"`
	Usage  json.RawMessage `json:"usage"`
}

// respItem is one item of a reply's output.
type respItem struct {
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
	// EncryptedContent is a reasoning item's reasoning, when it was asked
	// for (respRequest.Include).
	EncryptedContent string `json:"encrypted_content"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
}

// toResponses is req as a Responses request. A system message is the
// request's instructions; a tool result is the output of the call it answers;
// an assistant reply is its text, if it has any, and then its calls; its
// thinking stays out (above). A reply's reasoning blocks from origin go back
// as reasoning items, each where it came among the reply's calls; "" sends
// none.
func toResponses(req Request, origin string) respRequest {
	w := respRequest{Model: req.Model, MaxOutputTokens: req.MaxTokens, Temperature: req.Temperature, Seed: req.Seed}
	if req.Effort != "" {
		w.Reasoning = &respReasoning{Effort: req.Effort}
	}
	for _, m := range req.Messages {
		if m.Role == RoleSystem {
			// The window sends one, first; any more are joined, so none
			// is dropped.
			if w.Instructions != "" {
				w.Instructions += "\n\n"
			}
			w.Instructions += m.Content
			continue
		}
		if m.Role == RoleTool {
			w.Input = append(w.Input, respOutput{Type: "function_call_output", CallID: m.ToolCallID, Output: m.Content})
			continue
		}
		reasoning := func(before int) {
			for _, b := range m.Blocks {
				if origin != "" && b.Type == "reasoning" && b.Origin == origin && b.Before == before && b.Data != "" {
					item := respReasoningItem{Type: "reasoning", Summary: []respSummary{}, EncryptedContent: b.Data}
					if b.Thinking != "" {
						item.Summary = append(item.Summary, respSummary{Type: "summary_text", Text: b.Thinking})
					}
					w.Input = append(w.Input, item)
				}
			}
		}
		reasoning(0)
		// A reply that only calls tools is its calls.
		if m.Content != "" || len(m.ToolCalls) == 0 {
			w.Input = append(w.Input, respMessage{Type: "message", Role: m.Role, Content: responsesContent(m)})
		}
		for i, tc := range m.ToolCalls {
			if i > 0 {
				reasoning(i)
			}
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
	wire := toResponses(req, o.ReasoningOrigin)
	if o.ReasoningOrigin != "" {
		wire.Include = []string{"reasoning.encrypted_content"}
		if wire.Reasoning == nil {
			wire.Reasoning = &respReasoning{}
		}
		wire.Reasoning.Context = "all_turns"
	}
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
			var summary []string
			for _, s := range item.Summary {
				summary = append(summary, s.Text)
			}
			thinking = append(thinking, summary...)
			for _, c := range item.Content {
				thinking = append(thinking, c.Text)
			}
			if o.ReasoningOrigin != "" && item.EncryptedContent != "" {
				out.Message.Blocks = append(out.Message.Blocks, ThinkingBlock{Type: "reasoning", Thinking: strings.Join(summary, "\n\n"),
					Data: item.EncryptedContent, Before: len(out.Message.ToolCalls), Origin: o.ReasoningOrigin})
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
// Compatible servers that answer JSON are still accepted. A reply is JSON
// only if it opens as an object: the request asks for a stream, and a ChatGPT
// plan's server sent one that its Content-Type did not name, which was read as
// JSON ("invalid character 'e'", session 20261007T165642Z-97ee33d1).
func readResponses(resp *http.Response) (respResponse, error) {
	var out respResponse
	contentType := resp.Header.Get("Content-Type")
	body := bufio.NewReader(io.LimitReader(resp.Body, 32<<20))
	if !strings.HasPrefix(contentType, "text/event-stream") && startsJSON(body) {
		if err := json.NewDecoder(body).Decode(&out); err != nil {
			return out, fmt.Errorf("openai: bad response: %w", err)
		}
		return out, nil
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	var data []string
	// items are the output items the stream finished one by one. A ChatGPT
	// plan's server ends with a response whose output is empty, so its reply
	// is only these: read from the terminal response alone, every reply was
	// empty though the model wrote 160 tokens (session
	// 20261007T170623Z-89018e45).
	type doneItem struct {
		index int
		item  respItem
	}
	var items []doneItem
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
			Type        string       `json:"type"`
			Response    respResponse `json:"response"`
			OutputIndex int          `json:"output_index"`
			Item        respItem     `json:"item"`
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return false, errors.New("openai: malformed Responses stream event")
		}
		switch event.Type {
		case "response.output_item.done":
			items = append(items, doneItem{event.OutputIndex, event.Item})
		case "response.completed", "response.incomplete", "response.failed":
			out = event.Response
			if out.Status != strings.TrimPrefix(event.Type, "response.") {
				return false, errors.New("openai: Responses terminal event has the wrong status")
			}
			// The terminal response's own output is the whole reply when it
			// has any; the items stand in only for an empty one.
			if len(out.Output) == 0 {
				slices.SortStableFunc(items, func(a, b doneItem) int { return a.index - b.index })
				for _, d := range items {
					out.Output = append(out.Output, d.item)
				}
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
	// The header is named, not the body: the body may hold what redaction
	// has not seen.
	return out, fmt.Errorf("openai: Responses stream ended without a terminal response (Content-Type %q)", contentType)
}

// startsJSON says whether r's first byte after white space opens a JSON
// object, without reading it.
func startsJSON(r *bufio.Reader) bool {
	for n := 1; ; n++ {
		b, err := r.Peek(n)
		if err != nil {
			return false
		}
		switch c := b[n-1]; c {
		case ' ', '\t', '\r', '\n':
		default:
			return c == '{'
		}
	}
}
