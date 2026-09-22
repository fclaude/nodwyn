// Package loop is the agent loop from the spec: ask the model, run the tool
// it asked for through the guard, feed the result back, ask again. It stops
// on a plain answer or a hard stop. No sub-agents.
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
	"github.com/fclaude/nibb/internal/window"
)

// Host is what the loop needs from its owner (the core session): a way to
// record log events, the events so far (the window is rebuilt from them),
// and a way to emit ephemeral events to the front-end.
type Host interface {
	Record(kind log.Kind, payload any) (log.Event, error)
	EventsSoFar() []log.Event
	Emit(ev event.Event)
}

// Config bounds the loop.
type Config struct {
	Model          string
	NumCtx         int
	KeepAlive      string
	Window         window.Policy
	MaxToolCalls   int
	RequestTimeout time.Duration
	System         string
}

// Loop runs turns.
type Loop struct {
	host     Host
	provider llm.Provider
	interp   *scheme.Interp
	cfg      Config
	tools    []llm.Tool
	toolsLen int
}

// New builds a loop. The tool list is computed once: it must not change
// mid-session (ADR 9).
func New(host Host, provider llm.Provider, interp *scheme.Interp, cfg Config) *Loop {
	l := &Loop{host: host, provider: provider, interp: interp, cfg: cfg}
	for _, p := range interp.Tools() {
		l.tools = append(l.tools, llm.Tool{Name: p.Name, Description: p.Meta.Doc, Parameters: p.Schema()})
	}
	b, _ := json.Marshal(l.tools)
	l.toolsLen = window.Estimate(string(b))
	return l
}

// Tools returns the tool list offered to the model.
func (l *Loop) Tools() []llm.Tool { return l.tools }

// ErrHardStop is returned when a turn exceeds MaxToolCalls. The turn's work
// is in the log; a "continue" message starts a fresh budget.
var ErrHardStop = errors.New("hard stop: tool-call limit for this turn reached (loop.max_tool_calls / --max-tool-calls); say \"continue\" to go on")

// Turn handles one user message to completion.
func (l *Loop) Turn(ctx context.Context, text string) error {
	if _, err := l.host.Record(log.KindUserMsg, log.UserMsg{Text: text}); err != nil {
		return err
	}
	calls := 0
	for {
		w := window.Build(l.host.EventsSoFar(), l.cfg.System, l.toolsLen, l.cfg.Window)
		if _, err := l.host.Record(log.KindLLMReq, log.LLMReq{
			Model: l.cfg.Model, Messages: len(w.Messages), Tools: len(l.tools), EstTokens: w.EstTokens, Elided: w.Elided,
		}); err != nil {
			return err
		}
		l.host.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "busy", Detail: "waiting for model"}})

		reqCtx, cancel := context.WithTimeout(ctx, l.cfg.RequestTimeout)
		resp, err := l.provider.Chat(reqCtx, llm.Request{
			Model: l.cfg.Model, Messages: w.Messages, Tools: l.tools, NumCtx: l.cfg.NumCtx, KeepAlive: l.cfg.KeepAlive,
		}, func(d llm.Delta) {
			if d.Thinking != "" {
				l.host.Emit(event.Event{Kind: event.StreamThinking, Payload: event.Text{Text: d.Thinking}})
			}
			if d.Text != "" {
				l.host.Emit(event.Event{Kind: event.StreamText, Payload: event.Text{Text: d.Text}})
			}
		})
		cancel()
		if err != nil {
			_, _ = l.host.Record(log.KindNote, log.Note{Text: "model request failed: " + err.Error()})
			return err
		}
		respRec := log.LLMResp{
			Content: resp.Message.Content, Thinking: resp.Message.Thinking, DoneReason: resp.DoneReason,
			Usage: log.Usage{
				PromptTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.OutputTokens,
				PromptEvalMS: resp.Usage.PromptEvalMS, EvalMS: resp.Usage.EvalMS, TotalMS: resp.Usage.TotalMS, LoadMS: resp.Usage.LoadMS,
			},
		}
		for _, tc := range resp.Message.ToolCalls {
			respRec.ToolCalls = append(respRec.ToolCalls, log.ToolCall{ID: tc.ID, Name: tc.Name, Args: tc.Args})
		}
		if _, err := l.host.Record(log.KindLLMResp, respRec); err != nil {
			return err
		}
		if len(resp.Message.ToolCalls) == 0 {
			return nil
		}
		for _, tc := range resp.Message.ToolCalls {
			calls++
			if calls > l.cfg.MaxToolCalls {
				_, _ = l.host.Record(log.KindNote, log.Note{Text: fmt.Sprintf("hard stop after %d tool calls", calls-1)})
				return ErrHardStop
			}
			if err := l.runTool(ctx, tc); err != nil {
				return err
			}
		}
	}
}

// runTool evaluates one tool call as a Scheme application (ADR 3) with
// model origin, and records the result the model will see. Only context
// cancellation is an error here; every other failure is a tool result.
func (l *Loop) runTool(ctx context.Context, tc llm.ToolCall) error {
	if _, err := l.host.Record(log.KindToolCall, log.ToolCallEvent{
		ToolCall: log.ToolCall{ID: tc.ID, Name: tc.Name, Args: tc.Args}, Origin: string(scheme.OriginModel),
	}); err != nil {
		return err
	}
	l.host.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "busy", Detail: "running " + tc.Name}})
	start := time.Now()
	res := log.ToolResult{ID: tc.ID, Name: tc.Name}
	value, err := l.apply(ctx, tc)
	res.Millis = time.Since(start).Milliseconds()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		res.OK = false
		res.Error = err.Error()
	} else {
		res.OK = true
		res.Value = value
	}
	_, err = l.host.Record(log.KindToolResult, res)
	return err
}

func (l *Loop) apply(ctx context.Context, tc llm.ToolCall) (string, error) {
	p, ok := l.interp.Lookup(tc.Name)
	if !ok || !p.Tool() {
		return "", fmt.Errorf("unknown tool %q; available: %s", tc.Name, l.toolNames())
	}
	args, err := p.ArgsFromJSON(tc.Args)
	if err != nil {
		return "", err
	}
	v, err := l.interp.Apply(scheme.WithOrigin(ctx, scheme.OriginModel), scheme.Value{Kind: scheme.KPrim, Prim: p}, args)
	if err != nil {
		return "", err
	}
	return render(v), nil
}

// render turns a tool's value into what the model reads: strings as-is,
// void as "ok", everything else as JSON (falling back to the written form).
func render(v scheme.Value) string {
	switch v.Kind {
	case scheme.KString:
		return v.S
	case scheme.KVoid:
		return "ok"
	}
	if b, err := scheme.ToJSON(v); err == nil {
		return string(b)
	}
	return scheme.Write(v)
}

func (l *Loop) toolNames() string {
	names := make([]string, len(l.tools))
	for i, t := range l.tools {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}
