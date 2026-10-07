package loop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/scheme"
	"github.com/fclaude/nodwyn/internal/window"
)

// toolOffer is which of the session's tools a request offers: all of them
// (the zero value), none, or only the named ones.
type toolOffer struct {
	none bool
	only []string
}

// parseToolOffer reads model-turn's tools option: #f offers no tools, #t
// all of them, a list of tool names only those; anything else is an error.
func (l *Loop) parseToolOffer(v scheme.Value) (toolOffer, error) {
	if scheme.Equal(v, scheme.False) {
		return toolOffer{none: true}, nil
	}
	if scheme.Equal(v, scheme.True) {
		return toolOffer{}, nil
	}
	items, ok := v.Slice()
	if !ok {
		return toolOffer{}, fmt.Errorf("the tools option is #f (no tools), #t (all) or a list of tool names; got %s", scheme.Write(v))
	}
	if len(items) == 0 {
		return toolOffer{none: true}, nil
	}
	known := make(map[string]bool, len(l.tools))
	for _, t := range l.tools {
		known[t.Name] = true
	}
	only := make([]string, 0, len(items))
	for _, it := range items {
		if it.Kind != scheme.KString {
			return toolOffer{}, fmt.Errorf("the tools option is #f (no tools), #t (all) or a list of tool names as strings; got %s", scheme.Write(v))
		}
		if !known[it.S] {
			return toolOffer{}, fmt.Errorf("no tool named %q; the tools are %s", it.S, l.toolNames())
		}
		only = append(only, it.S)
	}
	return toolOffer{only: only}, nil
}

// offered is the part of the session's tool catalog an offer names, in
// catalog order.
func offered(catalog []llm.Tool, offer toolOffer) []llm.Tool {
	switch {
	case offer.none:
		return nil
	case offer.only == nil:
		return catalog
	}
	var out []llm.Tool
	for _, t := range catalog {
		if slices.Contains(offer.only, t.Name) {
			out = append(out, t)
		}
	}
	return out
}

// ask sends one request to a model and records both sides: the registry's
// model of that name (ControlModel for the control model), offering the
// session's tools that offer names. The window is rebuilt from the log
// every time, so the model sees exactly what has been recorded, and the
// request's hash lets nodwyn replay --window prove it. With headroom the
// window is trimmed only past the ceiling instead of the high water.
// Attached images are read from the workspace here, or withheld (images.go).
func (l *Loop) ask(ctx context.Context, name string, offer toolOffer, headroom bool) (*llm.Response, error) {
	ep, err := l.endpoint(name)
	if err != nil {
		return nil, err
	}
	pol, ceiling := l.policy()
	highWater := pol.HighWater
	if headroom && ceiling > pol.HighWater {
		pol.HighWater = ceiling
	}
	w := window.Build(l.host.EventsSoFar(), l.system, l.toolsLen, pol)
	if !hasUserMessage(w.Messages) {
		return nil, errors.New("the context has nothing for the model to answer; after narrow-context, add-message first")
	}
	withheld, err := l.images(ctx, name, ep, &w)
	if err != nil {
		return nil, err
	}
	// A model's own cap is its max_output, or a quarter of a small context
	// (config.Window.ReplyCap): either is the reply cap Settings › Models
	// shows for it.
	maxOutput, capFrom := l.cfg.MaxOutput, "window.output_reserve"
	if ep.MaxOutput > 0 {
		maxOutput, capFrom = ep.MaxOutput, "the reply cap of model "+name
	}
	request := llm.Request{Model: ep.Model, Messages: w.Messages, NumCtx: ep.NumCtx, KeepAlive: ep.KeepAlive, MaxTokens: maxOutput,
		Temperature: ep.Temperature, Seed: ep.Seed, Effort: ep.Effort}
	request.Tools = offered(l.tools, offer)
	digest, err := window.RequestHash(request)
	if err != nil {
		return nil, err
	}
	rec := log.LLMReq{
		RequestHash: digest,
		Model:       ep.Model, Messages: len(w.Messages), Tools: len(request.Tools), EstTokens: w.EstTokens, Elided: w.Elided,
		NoTools: offer.none, OfferedTools: offer.only, MaxOutput: ep.MaxOutput,
		Temperature: ep.Temperature, Seed: ep.Seed, Effort: ep.Effort, Withheld: withheld,
	}
	if pol.HighWater != highWater {
		rec.HighWater = pol.HighWater
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
		// A refusal that shows a configuration problem says what to do
		// about it, in the log and in the error the turn ends with.
		if hint := llm.Hint(err); hint != "" {
			err = fmt.Errorf("%w. %s", err, hint)
		}
		_ = l.record(log.KindNote, log.Note{Text: "model request failed: " + err.Error()})
		return nil, err
	}
	notes := l.tidyCalls(resp)
	l.show(resp)
	if err := l.record(log.KindLLMResp, responseRecord(resp, ep.Rates)); err != nil {
		return nil, err
	}
	for _, text := range notes {
		_ = l.record(log.KindNote, log.Note{Text: text})
	}
	if resp.DoneReason == "length" {
		// The reply is cut short; a strategy decides what to do about it
		// (ask-model continues one, strategy.scm), the log says it happened.
		note := fmt.Sprintf("the reply stopped at the output limit (%d tokens, %s)", maxOutput, capFrom)
		if maxOutput == 0 {
			note = "the reply stopped at the server's own output limit (nodwyn sets no cap: window.output_reserve is 0)"
		}
		_ = l.record(log.KindNote, log.Note{Text: note})
	}
	return resp, nil
}

// tidyCalls puts the arguments of the reply's calls that came wrapped in a
// JSON string in the plain shape (scheme.TidyArgs; ADR 29, amended
// 2026-10-06) before anything reads them: the log, the strategy and the
// tool see one form, and the history the model reads next shows its call
// as the tool took it. It returns a note for each call it changed, which
// ask records after the reply, so the log says it happened.
func (l *Loop) tidyCalls(resp *llm.Response) []string {
	var notes []string
	for i, tc := range resp.Message.ToolCalls {
		p, ok := l.interp.Lookup(tc.Name)
		if !ok || !p.Tool() {
			continue
		}
		if args, note := p.TidyArgs(tc.Args); note != "" {
			resp.Message.ToolCalls[i].Args = args
			notes = append(notes, fmt.Sprintf("%s call %s: %s; read as the plain call", tc.Name, tc.ID, note))
		}
	}
	return notes
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
	l.controlMu.RLock()
	ep, ok := l.cfg.Models[name]
	if ok {
		l.controlMu.RUnlock()
		return ep, nil
	}
	names := make([]string, 0, len(l.cfg.Models))
	for n := range l.cfg.Models {
		names = append(names, n)
	}
	l.controlMu.RUnlock()
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

// responseRecord is the llm.resp log payload for a response, priced with
// the model's rates when the provider did not report its cost.
func responseRecord(resp *llm.Response, rates llm.Rates) log.LLMResp {
	cost, estimated := llm.Cost(resp.Usage, rates)
	rec := log.LLMResp{
		Content: resp.Message.Content, Thinking: resp.Message.Thinking, DoneReason: resp.DoneReason,
		Usage: log.Usage{
			PromptTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.OutputTokens,
			PromptEvalMS: resp.Usage.PromptEvalMS, EvalMS: resp.Usage.EvalMS, TotalMS: resp.Usage.TotalMS, LoadMS: resp.Usage.LoadMS,
			WallMS:       resp.Usage.WallMS,
			CachedTokens: resp.Usage.CachedTokens, CacheWriteTokens: resp.Usage.CacheWriteTokens, ReasoningTokens: resp.Usage.ReasoningTokens,
			Cost: cost, CostEstimated: estimated, Unreported: resp.Usage.Unreported,
		},
	}
	for _, tc := range resp.Message.ToolCalls {
		rec.ToolCalls = append(rec.ToolCalls, log.ToolCall{ID: tc.ID, Name: tc.Name, Args: tc.Args})
	}
	for _, b := range resp.Message.Blocks {
		rec.Blocks = append(rec.Blocks, log.ThinkingBlock(b))
	}
	return rec
}
