package loop

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
	"github.com/fclaude/nibb/internal/window"
)

// ask sends one request to a model and records both sides: the registry's
// model of that name (ControlModel for the control model), offering the
// session's tools unless tools is false. The
// window is rebuilt from the log every time, so the model sees exactly what
// has been recorded, and the request's hash lets nibb replay --window prove
// it.
func (l *Loop) ask(ctx context.Context, name string, tools bool) (*llm.Response, error) {
	ep, err := l.endpoint(name)
	if err != nil {
		return nil, err
	}
	w := window.Build(l.host.EventsSoFar(), l.system, l.toolsLen, l.cfg.Window)
	if !hasUserMessage(w.Messages) {
		return nil, errors.New("the context has nothing for the model to answer; after narrow-context, add-message first")
	}
	maxOutput, capFrom := l.cfg.MaxOutput, "window.output_reserve"
	if ep.MaxOutput > 0 {
		maxOutput, capFrom = ep.MaxOutput, "max_output of model "+name
	}
	request := llm.Request{Model: ep.Model, Messages: w.Messages, NumCtx: ep.NumCtx, KeepAlive: ep.KeepAlive, MaxTokens: maxOutput}
	if tools {
		request.Tools = l.tools
	}
	digest, err := window.RequestHash(request)
	if err != nil {
		return nil, err
	}
	rec := log.LLMReq{
		RequestHash: digest,
		Model:       ep.Model, Messages: len(w.Messages), Tools: len(request.Tools), EstTokens: w.EstTokens, Elided: w.Elided,
		NoTools:   !tools,
		MaxOutput: ep.MaxOutput,
	}
	if name != ControlModel {
		// The session's recipe carries the control model's settings; any
		// other model's go with its request, for replay.
		rec.Endpoint, rec.NumCtx, rec.KeepAlive = name, ep.NumCtx, ep.KeepAlive
	}
	if err := l.record(log.KindLLMReq, rec); err != nil {
		return nil, err
	}
	l.host.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "busy", Detail: "waiting for model"}})

	attempt := func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithTimeout(parent, l.cfg.RequestTimeout)
	}
	resp, err := llm.ChatRetrying(ctx, ep.Provider, request, attempt, func(n int, err error) {
		_ = l.record(log.KindNote, log.Note{Text: fmt.Sprintf("model request failed, retrying (%d/%d): %s", n, llm.Retries, err)})
	})
	if err != nil {
		_ = l.record(log.KindNote, log.Note{Text: "model request failed: " + err.Error()})
		return nil, err
	}
	l.show(resp)
	if err := l.record(log.KindLLMResp, responseRecord(resp)); err != nil {
		return nil, err
	}
	if resp.DoneReason == "length" {
		// The reply is cut short; a strategy decides what to do about it
		// (ask-model continues one, strategy.scm), the log says it happened.
		note := fmt.Sprintf("the reply stopped at the output limit (%d tokens, %s)", maxOutput, capFrom)
		if maxOutput == 0 {
			note = "the reply stopped at the server's own output limit (nibb sets no cap: window.output_reserve is 0)"
		}
		_ = l.record(log.KindNote, log.Note{Text: note})
	}
	return resp, nil
}

func hasUserMessage(msgs []llm.Message) bool {
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			return true
		}
	}
	return false
}

// endpoint resolves a model name in the registry.
func (l *Loop) endpoint(name string) (Endpoint, error) {
	if ep, ok := l.cfg.Models[name]; ok {
		return ep, nil
	}
	names := make([]string, 0, len(l.cfg.Models))
	for n := range l.cfg.Models {
		names = append(names, n)
	}
	sort.Strings(names)
	return Endpoint{}, fmt.Errorf("no model %q is configured; configured models: %s", name, strings.Join(names, ", "))
}

// show displays a complete response. It is not streamed: a credential can
// be split across tokens, so only whole text can be redacted (ADR 25).
func (l *Loop) show(resp *llm.Response) {
	if resp.Message.Thinking != "" {
		l.host.Emit(event.Event{Kind: event.StreamThinking, Payload: event.Text{Text: redact.Redact(resp.Message.Thinking, nil).Text}})
	}
	if resp.Message.Content != "" {
		l.host.Emit(event.Event{Kind: event.StreamText, Payload: event.Text{Text: redact.Redact(resp.Message.Content, nil).Text}})
	}
}

// responseRecord is the llm.resp log payload for a response.
func responseRecord(resp *llm.Response) log.LLMResp {
	rec := log.LLMResp{
		Content: resp.Message.Content, Thinking: resp.Message.Thinking, DoneReason: resp.DoneReason,
		Usage: log.Usage{
			PromptTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.OutputTokens,
			PromptEvalMS: resp.Usage.PromptEvalMS, EvalMS: resp.Usage.EvalMS, TotalMS: resp.Usage.TotalMS, LoadMS: resp.Usage.LoadMS,
		},
	}
	for _, tc := range resp.Message.ToolCalls {
		rec.ToolCalls = append(rec.ToolCalls, log.ToolCall{ID: tc.ID, Name: tc.Name, Args: tc.Args})
	}
	return rec
}
