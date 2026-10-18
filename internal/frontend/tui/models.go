package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/log"
)

// modelView is what :model shows: the model the conversation talks to now,
// and the ones it may be moved to, as the console's picker offers them.
type modelView struct {
	Current core.ModelState
	Choices []modelOption
}

// modelOption is one choice: the name :model takes, the model it is and
// where, and the efforts it takes.
type modelOption struct {
	Name    string   `json:"name"`
	Model   string   `json:"model"`
	Host    string   `json:"host"`
	Efforts []string `json:"efforts"`
	Effort  string   `json:"effort"`
}

func (r *remote) Models(ctx context.Context, id string) (modelView, error) {
	var info struct {
		Model core.ModelState `json:"model"`
	}
	if err := r.client.Call(ctx, "session.info", map[string]any{"id": id}, &info); err != nil {
		return modelView{}, err
	}
	var settings struct {
		Choices []modelOption `json:"choices"`
	}
	if err := r.client.Call(ctx, "settings.read", struct{}{}, &settings); err != nil {
		return modelView{}, err
	}
	return modelView{Current: info.Model, Choices: settings.Choices}, nil
}

func (r *remote) SetModel(ctx context.Context, id, name, effort string) (state core.ModelState, err error) {
	err = r.client.Call(ctx, "session.model.set", map[string]any{"id": id, "name": name, "effort": effort}, &state)
	return
}

func formatModels(v modelView) string {
	var out strings.Builder
	c := v.Current
	fmt.Fprintf(&out, "This conversation talks to %s\n", modelLabel(c.Name, c.Model, c.Server))
	switch {
	case len(c.Efforts) == 0:
		out.WriteString("Effort: none; this model takes no effort setting.\n")
	case c.Effort == "":
		fmt.Fprintf(&out, "Effort: the provider's default (takes %s)\n", strings.Join(c.Efforts, ", "))
	default:
		fmt.Fprintf(&out, "Effort: %s (takes %s)\n", c.Effort, strings.Join(c.Efforts, ", "))
	}
	if q := c.Queued; q != nil {
		fmt.Fprintf(&out, "Queued for when this turn ends: %s%s\n", modelLabel(q.Name, q.Model, q.Server), effortSuffix(q.Effort))
	}
	if c.Unavailable != "" {
		fmt.Fprintf(&out, "Not available: %s. Choose a model before sending again.\n", c.Unavailable)
	}
	if c.Notice != "" {
		fmt.Fprintf(&out, "The last choice was not applied: %s\n", c.Notice)
	}
	if c.ContextUnknown != "" {
		fmt.Fprintf(&out, "Context size unknown: %s\n", c.ContextUnknown)
	}
	out.WriteString("\nChoices\n")
	if len(v.Choices) == 0 {
		out.WriteString("  None configured. Add models in config.toml or the console's Settings › Models.\n")
	}
	for _, o := range v.Choices {
		mark := "  "
		if o.Name == c.Name {
			mark = "› "
		}
		fmt.Fprintf(&out, "%s%s · %s on %s", mark, o.Name, o.Model, o.Host)
		if len(o.Efforts) > 0 {
			fmt.Fprintf(&out, " · efforts %s", strings.Join(o.Efforts, ", "))
			if o.Effort != "" {
				fmt.Fprintf(&out, " (starts at %s)", o.Effort)
			}
		}
		out.WriteByte('\n')
	}
	out.WriteString("\n:model NAME [EFFORT] moves the conversation from its next turn; a choice made while a turn runs waits for it to end. Naming the current model changes only the effort.")
	return out.String()
}

// modelLabel names a model as a person chose it: by its name when it has
// one, with the model id when that says more.
func modelLabel(name, model, server string) string {
	label := model
	if name != "" && name != model {
		label = name + " (" + model + ")"
	}
	if server != "" {
		label += " on " + server
	}
	return label
}

func effortSuffix(effort string) string {
	if effort == "" {
		return ""
	}
	return " · effort " + effort
}

// modelLine is a session.model record in the conversation: a switch, or a
// choice waiting for the turn under way.
func modelLine(p log.SessionModel) string {
	line := "model → " + modelLabel(p.Name, p.Model, p.Server) + effortSuffix(p.Effort)
	if p.Queued {
		line = "model choice queued for when this turn ends: " + modelLabel(p.Name, p.Model, p.Server) + effortSuffix(p.Effort)
	}
	if p.Actor != "" {
		line += " · by " + p.Actor
	}
	return line
}
