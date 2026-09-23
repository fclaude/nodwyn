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

	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
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
func New(host Host, provider llm.Provider, interp *scheme.Interp, cfg Config) (*Loop, error) {
	l := &Loop{host: host, provider: provider, interp: interp, cfg: cfg}
	for _, p := range interp.Tools() {
		l.tools = append(l.tools, llm.Tool{Name: p.Name, Description: p.Meta.Doc, Parameters: p.Schema()})
	}
	clean, _, err := redact.Payload(l.tools, nil)
	if err != nil {
		return nil, fmt.Errorf("loop: redact tool schemas: %w", err)
	}
	l.tools = clean.([]llm.Tool)
	l.cfg.System = redact.Redact(l.cfg.System, nil).Text
	b, _ := json.Marshal(l.tools)
	l.toolsLen = window.Estimate(string(b))
	return l, nil
}

// Tools returns the tool list offered to the model.
func (l *Loop) Tools() []llm.Tool { return l.tools }

// ErrHardStop is returned when a turn exceeds MaxToolCalls. The turn's work
// is in the log; a "continue" message starts a fresh budget.
var ErrHardStop = errors.New("hard stop: tool-call limit for this turn reached (loop.max_tool_calls / --max-tool-calls); say \"continue\" to go on")

// Turn handles the user message already recorded by the session to completion.
func (l *Loop) Turn(ctx context.Context) error {
	calls := 0
	var progress turnProgress
	turnBudget := budget.From(ctx)
	for {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if turnBudget != nil {
			if err := turnBudget.Check(ctx); err != nil {
				return err
			}
		}
		w := window.Build(l.host.EventsSoFar(), l.cfg.System, l.toolsLen, l.cfg.Window)
		request := llm.Request{Model: l.cfg.Model, Messages: w.Messages, Tools: l.tools, NumCtx: l.cfg.NumCtx, KeepAlive: l.cfg.KeepAlive}
		digest, err := window.RequestHash(request)
		if err != nil {
			return err
		}
		if _, err := l.host.Record(log.KindLLMReq, log.LLMReq{
			RequestHash: digest,
			Model:       l.cfg.Model, Messages: len(w.Messages), Tools: len(l.tools), EstTokens: w.EstTokens, Elided: w.Elided,
		}); err != nil {
			return err
		}
		l.host.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "busy", Detail: "waiting for model"}})

		reqCtx, cancel := context.WithTimeout(ctx, l.cfg.RequestTimeout)
		resp, err := l.provider.Chat(reqCtx, request, nil)
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
		// Buffer the response: tokens can split a credential at any byte.
		if resp.Message.Thinking != "" {
			l.host.Emit(event.Event{Kind: event.StreamThinking, Payload: event.Text{Text: redact.Redact(resp.Message.Thinking, nil).Text}})
		}
		if resp.Message.Content != "" {
			l.host.Emit(event.Event{Kind: event.StreamText, Payload: event.Text{Text: redact.Redact(resp.Message.Content, nil).Text}})
		}
		if _, err := l.host.Record(log.KindLLMResp, respRec); err != nil {
			return err
		}
		if turnBudget != nil {
			turnBudget.AddTokens(resp.Usage.PromptTokens, resp.Usage.OutputTokens)
		}
		if len(resp.Message.ToolCalls) == 0 {
			if turnBudget != nil {
				return turnBudget.Finish(ctx)
			}
			return nil
		}
		for i, tc := range resp.Message.ToolCalls {
			var limitErr error
			if turnBudget != nil {
				limitErr = turnBudget.ToolCall(ctx)
			} else {
				calls++
				if calls > l.cfg.MaxToolCalls {
					limitErr = ErrHardStop
				}
			}
			if limitErr != nil {
				if turnBudget == nil {
					if _, err := l.host.Record(log.KindNote, log.Note{Text: limitErr.Error()}); err != nil {
						return err
					}
				}
				if err := l.skipTools(resp.Message.ToolCalls[i:], limitErr.Error()); err != nil {
					return err
				}
				return limitErr
			}
			outcome, err := l.runTool(ctx, tc)
			if err != nil {
				if recordErr := l.skipTools(resp.Message.ToolCalls[i+1:], err.Error()); recordErr != nil {
					return recordErr
				}
				return err
			}
			if reason := progress.observe(tc, outcome); reason != "" {
				err := fmt.Errorf("progress stop: %s; inspect the results before continuing", reason)
				if _, recordErr := l.host.Record(log.KindNote, log.Note{Text: err.Error()}); recordErr != nil {
					return recordErr
				}
				if recordErr := l.skipTools(resp.Message.ToolCalls[i+1:], err.Error()); recordErr != nil {
					return recordErr
				}
				return err
			}
		}
	}
}

// runTool evaluates one tool call as a Scheme application (ADR 3) with
// model origin, and records the result the model will see. Only context
// cancellation or recording failure is returned as an error; operation failures
// are tool results, with a typed outcome for turn policy.
func (l *Loop) runTool(ctx context.Context, tc llm.ToolCall) (toolOutcome, error) {
	var outcome toolOutcome
	if _, err := l.host.Record(log.KindToolCall, log.ToolCallEvent{
		ToolCall: log.ToolCall{ID: tc.ID, Name: tc.Name, Args: tc.Args}, Origin: string(scheme.OriginModel),
	}); err != nil {
		return outcome, err
	}
	l.host.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "busy", Detail: "running " + tc.Name}})
	start := time.Now()
	res := log.ToolResult{ID: tc.ID, Name: tc.Name}
	value, err := l.apply(ctx, tc)
	res.Millis = time.Since(start).Milliseconds()
	if err != nil {
		if ctx.Err() != nil {
			cause := context.Cause(ctx)
			res.Error = "Interrupted during execution; outcome may be partial: " + cause.Error()
			if _, recordErr := l.host.Record(log.KindToolResult, res); recordErr != nil {
				return outcome, recordErr
			}
			return outcome, cause
		}
		outcome.failed = true
		var denied *scheme.Denied
		outcome.denied = errors.As(err, &denied)
		res.OK = false
		res.Error = err.Error()
	} else {
		res.OK = true
		res.Value = value
	}
	_, err = l.host.Record(log.KindToolResult, res)
	return outcome, err
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

// skipTools completes the transcript for calls that a policy stop prevented.
// No Scheme evaluation or side effect occurs for these calls.
func (l *Loop) skipTools(calls []llm.ToolCall, reason string) error {
	for _, call := range calls {
		if _, err := l.host.Record(log.KindToolResult, log.ToolResult{ID: call.ID, Name: call.Name, OK: false, Error: "Not executed: " + reason}); err != nil {
			return err
		}
	}
	return nil
}

// WindowContext is the immutable, redacted recipe needed to replay requests.
// Tool schema marshaling already succeeded in New.
func (l *Loop) WindowContext() *log.WindowContext {
	tools, _ := json.Marshal(l.tools)
	return &log.WindowContext{Version: window.RecipeVersion, System: l.cfg.System, Tools: tools, HighWater: l.cfg.Window.HighWater, LowWater: l.cfg.Window.LowWater, MaxResultTokens: l.cfg.Window.MaxResultTokens, NumCtx: l.cfg.NumCtx, KeepAlive: l.cfg.KeepAlive}
}
