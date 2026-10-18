package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
)

// :judge shows the judge the task asks and the judges set up, with each
// one's model and price; :judge NAME and :judge none move it (ADR 47).
// Nothing here asks a judge anything.
func TestJudgeChoiceThroughTheDaemon(t *testing.T) {
	o := testOptions(t)
	o.Config.Servers = map[string]config.Server{
		"jev":    {Provider: "jev", URL: "https://api.typesafe.ai", Credential: "jev-key"},
		"second": {Provider: "jev", URL: "https://api.typesafe.ai", Credential: "second-key"},
	}
	o.Config.Judge.Server = "jev"
	kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range []string{"jev-key", "second-key"} {
		if _, err := kc.Add(class, []byte("sk-"+class+"-canary")); err != nil {
			t.Fatal(err)
		}
	}
	o.Keychain = kc
	b := testBackend(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	snap, err := b.Attach(ctx, o.Repo, "")
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(ctx, b, o.Repo, snap.ID, false)
	m.restore(snap)

	typed(m, ":judge")
	if m.inspector == nil || !strings.Contains(m.inspector.content, "This task asks jev: jev-latest at api.typesafe.ai, $0.042 per million input tokens, output free") ||
		!strings.Contains(m.inspector.content, "  second · jev-latest at api.typesafe.ai") {
		t.Fatalf(":judge:\n%v", m.inspector)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	typed(m, ":judge nowhere")
	if !strings.Contains(m.status, "not configured") {
		t.Fatalf("an unknown judge: %q", m.status)
	}
	typed(m, ":judge second")
	if m.status != "" {
		t.Fatalf("switch: %q", m.status)
	}
	pump(t, m, transcriptHas(m, "judge → second (jev-latest at api.typesafe.ai)"))
	typed(m, ":judge none")
	pump(t, m, transcriptHas(m, "judge → none"))
	typed(m, ":judge")
	if !strings.Contains(m.inspector.content, "This task has no judge") {
		t.Fatalf(":judge after none:\n%s", m.inspector.content)
	}
}
