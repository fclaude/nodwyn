package loop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// runTool evaluates one tool call as a Scheme application (ADR 3) with
// model origin, and records the result the model will see.
//
// A failed operation — denied, missing file, bad arguments — is a result,
// not an error: the model reads it and decides what to do. Only
// cancellation or a log failure is returned as an error. outcome classifies
// the result for the progress guard, and value is what a strategy receives:
// the recorded text, still carrying the provenance of what the tool
// produced (L2).
func (l *Loop) runTool(ctx context.Context, tc llm.ToolCall) (log.ToolResult, scheme.Value, toolOutcome, error) {
	var outcome toolOutcome
	res := log.ToolResult{ID: tc.ID, Name: tc.Name}
	if err := l.record(log.KindToolCall, log.ToolCallEvent{
		ToolCall: log.ToolCall{ID: tc.ID, Name: tc.Name, Args: tc.Args}, Origin: string(scheme.OriginModel),
	}); err != nil {
		return res, scheme.Void, outcome, err
	}
	l.host.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "busy", Detail: "running " + tc.Name}})
	start := time.Now()
	produced, err := l.apply(ctx, tc)
	res.Millis = time.Since(start).Milliseconds()
	if err != nil {
		if ctx.Err() != nil {
			cause := context.Cause(ctx)
			res.Error = "Interrupted during execution; outcome may be partial: " + cause.Error()
			if recordErr := l.record(log.KindToolResult, res); recordErr != nil {
				return res, scheme.Void, outcome, recordErr
			}
			return res, scheme.Void, outcome, cause
		}
		outcome.failed = true
		var denied *scheme.Denied
		outcome.denied = errors.As(err, &denied)
		res.OK = false
		res.Error = err.Error()
	} else {
		res.OK = true
		res.Value = render(produced)
	}
	err = l.record(log.KindToolResult, res)
	return res, scheme.Tainted(scheme.Str(res.Value), produced.Taint), outcome, err
}

// apply looks the tool up, orders the model's named JSON arguments into
// the primitive's positional parameters, and applies it with model origin:
// from here the call takes exactly the path a call from the REPL would,
// through the guard.
func (l *Loop) apply(ctx context.Context, tc llm.ToolCall) (scheme.Value, error) {
	p, ok := l.interp.Lookup(tc.Name)
	if !ok || !p.Tool() {
		if s := l.suggestTool(tc.Name); s != "" {
			return scheme.Void, fmt.Errorf("unknown tool %q; did you mean %s? available: %s", tc.Name, s, l.toolNames())
		}
		return scheme.Void, fmt.Errorf("unknown tool %q; available: %s", tc.Name, l.toolNames())
	}
	args, err := p.ArgsFromJSON(tc.Args)
	if err != nil {
		return scheme.Void, err
	}
	return l.interp.Apply(scheme.WithOrigin(ctx, scheme.OriginModel), scheme.Value{Kind: scheme.KPrim, Prim: p}, args)
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

// suggestTool names the tool a mistyped name most likely meant: the same
// name with - for _, or the one tool whose last word matches (fs-grep,
// index-grep); "" when there is no single candidate. Models invent names
// in the family's style (fs-grep for index-grep, fs_edit for fs-edit).
func (l *Loop) suggestTool(name string) string {
	norm := strings.ReplaceAll(strings.ToLower(name), "_", "-")
	last := norm[strings.LastIndex(norm, "-")+1:]
	var same []string
	for _, t := range l.tools {
		if t.Name == norm {
			return t.Name
		}
		if t.Name[strings.LastIndex(t.Name, "-")+1:] == last {
			same = append(same, t.Name)
		}
	}
	if len(same) == 1 {
		return same[0]
	}
	return ""
}

func (l *Loop) toolNames() string {
	names := make([]string, len(l.tools))
	for i, t := range l.tools {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}

// skipTools answers calls without running them: no Scheme evaluation, no
// side effect, only a result that says why.
func (l *Loop) skipTools(calls []llm.ToolCall, reason string) error {
	for _, call := range calls {
		if err := l.record(log.KindToolResult, log.ToolResult{ID: call.ID, Name: call.Name, OK: false, Error: "Not executed: " + reason}); err != nil {
			return err
		}
	}
	return nil
}
