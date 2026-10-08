package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/prim"
)

// judgeView is what :judge shows: the judge the task asks now, and the
// judges it may ask instead (ADR 47), as the console's picker offers them.
type judgeView struct {
	Current core.JudgeState
	Choices []judgeOption
}

// judgeOption is one judge: the name :judge takes, where its questions go,
// the model they ask and what they cost.
type judgeOption struct {
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	Host        string   `json:"host"`
	Model       string   `json:"model"`
	PriceInput  *float64 `json:"price_input"`
	PriceOutput *float64 `json:"price_output"`
	State       string   `json:"state"`
	Detail      string   `json:"detail"`
	Default     bool     `json:"default"`
}

func (l *local) Judges(ctx context.Context, _ string) (judgeView, error) {
	if err := ctx.Err(); err != nil {
		return judgeView{}, err
	}
	cfg := l.options.Config
	view := judgeView{Current: l.session.Judge()}
	for _, name := range cfg.JudgeChoices() {
		spec, _ := cfg.JudgeOn(name)
		view.Choices = append(view.Choices, judgeOption{Name: name, Provider: spec.Provider, Host: prim.JudgeHost(prim.JudgeOptions{URL: spec.URL}), Model: spec.Model, PriceInput: spec.PriceInput, PriceOutput: spec.PriceOutput, Default: name == cfg.Judge.Server})
	}
	return view, nil
}
func (r *remote) Judges(ctx context.Context, id string) (judgeView, error) {
	var info struct {
		Judge core.JudgeState `json:"judge"`
	}
	if err := r.client.Call(ctx, "session.info", map[string]any{"id": id}, &info); err != nil {
		return judgeView{}, err
	}
	var settings struct {
		Judges []judgeOption `json:"judges"`
	}
	if err := r.client.Call(ctx, "settings.read", struct{}{}, &settings); err != nil {
		return judgeView{}, err
	}
	return judgeView{Current: info.Judge, Choices: settings.Judges}, nil
}

func (l *local) SetJudge(_ context.Context, _, server string) (core.JudgeState, error) {
	return l.session.SetJudge(server, "terminal")
}
func (r *remote) SetJudge(ctx context.Context, id, server string) (state core.JudgeState, err error) {
	err = r.client.Call(ctx, "session.judge.set", map[string]any{"id": id, "judge": server}, &state)
	return
}

// judgePrice says what a judge's questions cost, as far as anyone says.
func judgePrice(in, out *float64) string {
	if in == nil || out == nil {
		return "price unknown"
	}
	if *out == 0 {
		return fmt.Sprintf("$%v per million input tokens, output free", *in)
	}
	return fmt.Sprintf("$%v in, $%v out per million tokens", *in, *out)
}

func formatJudges(v judgeView) string {
	var out strings.Builder
	c := v.Current
	switch {
	case c.Server == "":
		out.WriteString("This task has no judge: its plans are drafted without a score, and you answer its commands.\n")
	default:
		fmt.Fprintf(&out, "This task asks %s: %s at %s, %s\n", c.Server, c.Model, c.Host, judgePrice(c.PriceInput, c.PriceOutput))
		if !c.Available {
			fmt.Fprintf(&out, "It cannot be asked now: %s. You answer instead.\n", c.Why)
		}
	}
	out.WriteString("\nJudges\n")
	if len(v.Choices) == 0 {
		out.WriteString("  None set up. Add Jev or an OpenAI API key in the console's Settings › Judge.\n")
	}
	for _, o := range v.Choices {
		mark := "  "
		if o.Name == c.Server {
			mark = "› "
		}
		fmt.Fprintf(&out, "%s%s · %s at %s · %s", mark, o.Name, o.Model, o.Host, judgePrice(o.PriceInput, o.PriceOutput))
		if o.Default {
			out.WriteString(" · default")
		}
		if o.State != "" && o.State != "ready" {
			fmt.Fprintf(&out, " · %s", firstOf(o.Detail, o.State))
		}
		out.WriteByte('\n')
	}
	out.WriteString("\n:judge NAME asks that judge from the task's next question; :judge none asks none. It is chosen while the task is idle.")
	return out.String()
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// judgeLine is a session.judge record in the conversation.
func judgeLine(p log.SessionJudge) string {
	line := "judge → none: you answer the commands"
	if p.Server != "" {
		line = "judge → " + p.Server
		if p.Model != "" {
			line += " (" + p.Model + " at " + p.Host + ")"
		}
	}
	if p.Actor != "" {
		line += " · by " + p.Actor
	}
	return line
}
