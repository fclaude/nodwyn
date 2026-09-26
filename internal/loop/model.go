package loop

import (
	"context"

	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
	"github.com/fclaude/nibb/internal/window"
)

// ask sends one request to the model and records both sides. The window is
// rebuilt from the log every time, so the model sees exactly what has been
// recorded, and the request's hash lets nibb replay --window prove it.
func (l *Loop) ask(ctx context.Context) (*llm.Response, error) {
	w := window.Build(l.host.EventsSoFar(), l.cfg.System, l.toolsLen, l.cfg.Window)
	request := llm.Request{Model: l.cfg.Model, Messages: w.Messages, Tools: l.tools, NumCtx: l.cfg.NumCtx, KeepAlive: l.cfg.KeepAlive}
	digest, err := window.RequestHash(request)
	if err != nil {
		return nil, err
	}
	if err := l.record(log.KindLLMReq, log.LLMReq{
		RequestHash: digest,
		Model:       l.cfg.Model, Messages: len(w.Messages), Tools: len(l.tools), EstTokens: w.EstTokens, Elided: w.Elided,
	}); err != nil {
		return nil, err
	}
	l.host.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "busy", Detail: "waiting for model"}})

	reqCtx, cancel := context.WithTimeout(ctx, l.cfg.RequestTimeout)
	resp, err := l.provider.Chat(reqCtx, request, nil)
	cancel()
	if err != nil {
		_ = l.record(log.KindNote, log.Note{Text: "model request failed: " + err.Error()})
		return nil, err
	}
	l.show(resp)
	if err := l.record(log.KindLLMResp, responseRecord(resp)); err != nil {
		return nil, err
	}
	return resp, nil
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
