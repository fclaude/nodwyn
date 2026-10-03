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

// sizeWindow sizes the window for the control model's context
// (config.Window.Sized): its num_ctx, or, when that is unset, what an
// OpenAI-compatible server's model list says, which then fills num_ctx so
// the session's recipe and (model-info) report it. It returns the control
// model so filled, the window, its ceiling, and a note saying how the window
// was sized ("" when the defaults stand for a reason nothing needs saying).
// Opening a conversation and moving it to another model size it the same way
// (ADR 40).
func (s *Session) sizeWindow(control loop.Endpoint) (loop.Endpoint, config.Window, int, string) {
	w := s.opts.Config.Window
	if oai, ok := control.Provider.(*llm.OpenAI); ok && control.NumCtx == 0 {
		n, err := contextLength(oai, control.Model)
		if err != nil {
			return control, w, w.HighWater, fmt.Sprintf("the window keeps its default size: %s; set num_ctx in [control] to size it for the model's context", err)
		}
		control.NumCtx = n
	}
	reserve := w.OutputReserve
	if control.MaxOutput > 0 {
		reserve = control.MaxOutput
	}
	sized, ceiling := w.Sized(control.NumCtx, reserve)
	if sized.HighWater != w.HighWater || ceiling > sized.HighWater {
		return control, sized, ceiling, fmt.Sprintf("the window is sized for %s's context of %d tokens: trimmed past %d down to %d, compacted with room up to %d",
			control.Model, control.NumCtx, sized.HighWater, sized.LowWater, ceiling)
	}
	return control, sized, ceiling, ""
}

// note records a note in the session log; a note that cannot be written
// is dropped, like the loop's.
func (s *Session) note(text string) {
	_, _ = s.Record(log.KindNote, log.Note{Text: text})
}

// contextLengths holds what servers' model lists said, by URL and model, so
// a process that opens many sessions asks once.
var contextLengths sync.Map

// contextLength asks oai's model list for model's context length, briefly:
// a server that does not answer leaves the window at its default size.
func contextLength(oai *llm.OpenAI, model string) (int, error) {
	key := oai.URL + "\x00" + model
	if n, ok := contextLengths.Load(key); ok {
		return n.(int), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := oai.ContextLength(ctx, model)
	if err != nil {
		return 0, err
	}
	contextLengths.Store(key, n)
	return n, nil
}
