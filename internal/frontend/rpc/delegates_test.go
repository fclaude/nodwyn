package rpc

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
)

// Labels are written in Settings, read back with the choices, and reach a
// task that is already open; a task is opened with the models its model may
// hand work to, which a paired device may not choose.
func TestLabelsAndDelegates(t *testing.T) {
	opts := options(t)
	path, _ := start(t, opts)
	c := connect(t, path)
	c.hello()
	c.ok("settings.provider.write", map[string]any{"name": "p", "provider": "ollama", "url": "http://127.0.0.1:11434"})
	c.ok("settings.model.write", map[string]any{"name": "fast", "server": "p", "model": "small", "labels": []string{"fast", "cheap"}})
	c.ok("settings.model.write", map[string]any{"name": "deep", "server": "p", "model": "large"})

	labels := func() map[string][]string {
		var settings settingsResult
		into(t, c.ok("settings.read", map[string]any{}), &settings)
		out := map[string][]string{}
		for _, m := range settings.Choices {
			out[m.Name] = m.Labels
		}
		return out
	}
	if got := labels(); !slices.Equal(got["fast"], []string{"fast", "cheap"}) || len(got["deep"]) != 0 {
		t.Fatalf("labels after writing: %v", got)
	}
	// An edit that leaves labels out keeps them; [] clears them.
	c.ok("settings.model.write", map[string]any{"name": "fast", "server": "p", "model": "small", "efforts": []string{"low"}})
	if got := labels(); !slices.Equal(got["fast"], []string{"fast", "cheap"}) {
		t.Fatalf("an edit without labels changed them: %v", got)
	}
	if r := c.call("settings.model.write", map[string]any{"name": "fast", "server": "p", "model": "small", "labels": []string{"Not Valid"}}); r.Error == nil || !strings.Contains(r.Error.Message, "labels") {
		t.Fatalf("a badly spelled label: %+v", r.Error)
	}

	var task struct {
		ID        string   `json:"id"`
		Delegates []string `json:"delegates"`
	}
	into(t, c.ok("session.open", map[string]any{"repo": t.TempDir()}), &task)
	if len(task.Delegates) != 0 {
		t.Fatalf("a task opened with no choice may delegate to %v", task.Delegates)
	}
	var plain struct {
		ID string `json:"id"`
	}
	into(t, c.ok("session.open", map[string]any{"repo": t.TempDir(), "delegates": []string{"deep", "fast"}}), &plain)
	var info struct {
		Delegates []string `json:"delegates"`
	}
	into(t, c.ok("session.info", map[string]any{"id": plain.ID}), &info)
	if !slices.Equal(info.Delegates, []string{"deep", "fast"}) {
		t.Fatalf("delegates = %v", info.Delegates)
	}
	for _, bad := range []string{"nowhere", "control"} {
		if r := c.call("session.open", map[string]any{"repo": t.TempDir(), "delegates": []string{bad}}); r.Error == nil || !strings.Contains(r.Error.Message, "no model") {
			t.Fatalf("delegate %q: %+v", bad, r.Error)
		}
	}

	// A label edited now reaches the task opened before it.
	c.ok("settings.model.write", map[string]any{"name": "fast", "server": "p", "model": "small", "labels": []string{"careful"}})
	c.ok("session.subscribe", map[string]any{"id": plain.ID})
	eval(c, plain.ID, `(assoc-ref (model-info "fast") 'labels)`)
	if got := finishEval(t, c).Value; !strings.Contains(got, "careful") || strings.Contains(got, "cheap") {
		t.Fatalf("an open task reads labels %s", got)
	}

	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	if r := phone.call("session.open", map[string]any{"repo": t.TempDir(), "delegates": []string{"fast"}}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
		t.Fatalf("a device chose what a task may delegate to: %+v", r.Error)
	}

	// The default goes by its entry's labels, so an edit of them shows on it:
	// [control] keeps no copy of them to go stale.
	c.ok("settings.control.write", map[string]any{"name": "fast"})
	c.ok("settings.model.write", map[string]any{"server": "p", "model": "small", "labels": []string{"steady"}})
	if got := labels(); !slices.Equal(got["control"], []string{"steady"}) {
		t.Fatalf("the default's labels after its entry's were edited: %v", got)
	}
	raw, err := os.ReadFile(opts.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := config.Parse(raw); err != nil || len(cfg.Control.Labels) != 0 {
		t.Fatalf("[control] holds labels of its own (%v):\n%s", err, raw)
	}
}
