package loop

import (
	"context"
	"fmt"

	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
)

// TakesImages reports whether the named model takes images: ollama says so
// in /api/show, an openai model's configuration with vision = true. The
// provider is asked once per session; a provider that cannot say is taken
// not to.
func (l *Loop) TakesImages(ctx context.Context, name string) (bool, error) {
	ep, err := l.endpoint(name)
	if err != nil {
		return false, err
	}
	l.visionMu.Lock()
	known, ok := l.vision[name]
	l.visionMu.Unlock()
	if ok {
		return known, nil
	}
	reporter, ok := ep.Provider.(llm.VisionReporter)
	if !ok {
		return false, nil
	}
	vision, err := reporter.Vision(ctx, ep.Model)
	if err != nil {
		return false, err
	}
	l.visionMu.Lock()
	if l.vision == nil {
		l.vision = map[string]bool{}
	}
	l.vision[name] = vision
	l.visionMu.Unlock()
	return vision, nil
}

// images readies the window's attached images for a request to the model
// name (ADR 22 §3). An image goes only to the host the person confirmed it
// for, only to a model that takes images, and only as the bytes that were
// attached: each is read from the attachment store and checked against its
// digest, or withheld with a line saying why. The reasons go into the
// request's record, so replay rebuilds the request without the files.
func (l *Loop) images(ctx context.Context, name string, ep Endpoint, w *window.Result) ([]log.ImageGap, error) {
	var gaps []log.ImageGap
	vision, asked := false, false
	for _, ref := range w.Images {
		reason := ""
		switch {
		case ref.Destination != ep.Host:
			reason = window.ImageElsewhere
		default:
			if !asked {
				v, err := l.TakesImages(ctx, name)
				if err != nil {
					return nil, fmt.Errorf("asking whether model %s takes images: %w", name, err)
				}
				vision, asked = v, true
			}
			if !vision {
				reason = window.ImageNoVision
				break
			}
			data, why := window.ReadImage(l.cfg.Root, ref)
			if why != "" {
				reason = why
				break
			}
			w.SetImage(ref.Seq, data)
		}
		if reason != "" {
			gaps = append(gaps, log.ImageGap{Seq: ref.Seq, Reason: reason})
		}
	}
	w.Withhold(gaps)
	return gaps, nil
}
