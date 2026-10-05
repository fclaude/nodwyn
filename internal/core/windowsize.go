package core

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/loop"
)

// sizeWindow sizes the window for the control model's context less its
// reply cap (config.Window.Sized, ReplyCap): its num_ctx or what its
// provider publishes (endpoint fills both in), or, when neither says, what
// its server's model list says (an OpenAI-compatible server's, or
// Anthropic's), which then fills num_ctx so the session's recipe and
// (model-info) report it. It
// returns the control model so filled, its reply cap set when it is not the
// window's default, the window, its ceiling, and a note saying how the
// window was sized ("" when the defaults stand for a reason nothing needs
// saying).
// Opening a conversation and moving it to another model size it the same way
// (ADR 40).
func (s *Session) sizeWindow(control loop.Endpoint) (loop.Endpoint, config.Window, int, string) {
	w := s.opts.Config.Window
	if reporter, ok := control.Provider.(llm.ContextReporter); ok && control.NumCtx == 0 {
		n, err := contextLength(reporter, control.Server, control.Model)
		if err != nil {
			return control, w, w.HighWater, fmt.Sprintf("the window keeps its default size, since %s's context length is not known (%s); set it for the model in Settings › Models, or as num_ctx in its entry in config.toml", control.Model, err)
		}
		control.NumCtx = n
	}
	control.MaxOutput = s.replyCap(control.NumCtx, control.MaxOutput)
	reserve := w.ReplyCap(control.NumCtx, control.MaxOutput)
	if control.NumCtx > 0 && reserve >= control.NumCtx {
		return control, w, w.HighWater, fmt.Sprintf("the window keeps its default size, since %s's reply cap of %d tokens leaves nothing of its context of %d; lower the reply cap in Settings › Models, or max_output in its entry in config.toml", control.Model, reserve, control.NumCtx)
	}
	sized, ceiling := w.Sized(control.NumCtx, reserve)
	if sized.HighWater != w.HighWater || ceiling > sized.HighWater {
		return control, sized, ceiling, fmt.Sprintf("the window is sized for %s's context of %d tokens less its reply cap of %d: trimmed past %d down to %d, compacted with room up to %d",
			control.Model, control.NumCtx, reserve, sized.HighWater, sized.LowWater, ceiling)
	}
	return control, sized, ceiling, ""
}

// replyCap is the cap a model's requests carry when it is not the window's
// default (config.Window.ReplyCap): its own max_output, or a quarter of a
// small context. 0 leaves its requests at window.output_reserve, as a model
// that sets none always was, so such a session's log and replay are as
// before.
func (s *Session) replyCap(numCtx, maxOutput int) int {
	w := s.opts.Config.Window
	if c := w.ReplyCap(numCtx, maxOutput); c != w.OutputReserve {
		return c
	}
	return maxOutput
}

// note records a note in the session log; a note that cannot be written
// is dropped, like the loop's.
func (s *Session) note(text string) {
	_, _ = s.Record(log.KindNote, log.Note{Text: text})
}

// contextLengths holds what servers' model lists said, by server name, URL
// and model, so a process that opens many sessions asks once, and two servers
// at one address (an API key and a ChatGPT plan) do not share an answer.
var contextLengths sync.Map

// contextLength asks the provider's model list for model's context length,
// briefly: a server that does not answer leaves the window at its default size.
func contextLength(p llm.ContextReporter, server, model string) (int, error) {
	key := server + "\x00" + p.Server() + "\x00" + model
	if n, ok := contextLengths.Load(key); ok {
		return n.(int), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := p.ContextLength(ctx, model)
	if err != nil {
		return 0, err
	}
	contextLengths.Store(key, n)
	return n, nil
}
