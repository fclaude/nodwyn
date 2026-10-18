package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nodwyn/internal/frontend/workflows"
	"github.com/fclaude/nodwyn/internal/watch"
	"github.com/fclaude/nodwyn/internal/workflow"
)

// :workflows and :outbox show what the console shows, in the daemon's words,
// and act by the reference in brackets: pause and resume a workflow, discard a
// held answer. The daemon decides; the words a person sees are the ones the
// command line prints (ADR 53).
func TestWorkflowsAndTheOutboxThroughTheDaemon(t *testing.T) {
	o := testOptions(t)
	store := workflow.Store{Dir: filepath.Join(o.Paths.DataDir, "workflows")}
	id := workflow.NewID()
	def := workflow.Definition{Name: "Check staging", Every: 1440, Message: "Look at it.", Routing: workflow.OneTask, Repo: o.Repo, Source: watch.Source{Kind: watch.Command, Command: []string{"/usr/bin/true"}, Dir: o.Repo}}
	if _, err := store.Append(id, workflow.KindDefine, workflow.Define{Definition: def, Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	held := workflow.Outbox{ID: workflow.NewID(), Kind: "review", Item: "142", Task: "t-1", Summary: "Pull request #142 opened: fix: login loop", Body: "Needs a test for the empty case.", At: time.Now()}
	if _, err := store.Append(id, workflow.KindOutbox, held); err != nil {
		t.Fatal(err)
	}
	b := testBackend(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	snap, err := b.Attach(ctx, o.Repo, "")
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(ctx, b, o.Repo, snap.ID, false)
	m.restore(snap)
	view := func(command string) string {
		t.Helper()
		typed(m, command)
		if m.inspector == nil {
			t.Fatalf("%s showed nothing; status %q", command, m.status)
		}
		// The read runs beside the keyboard: its result arrives as a message.
		pump(t, m, func() bool { return !m.inspector.loading })
		content := m.inspector.content
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		return content
	}
	ref := workflows.Ref(id)
	got := view(":workflows")
	if !strings.Contains(got, "Check staging  ["+ref+"]") || !strings.Contains(got, "When /usr/bin/true prints a new or changed item") || !strings.Contains(got, "1 answer waiting to be posted") {
		t.Fatalf(":workflows:\n%s", got)
	}
	heldRef := workflows.Ref(held.ID)
	if got := view(":outbox"); !strings.Contains(got, "["+heldRef+"] Review for #142 · Check staging") || !strings.Contains(got, "> Needs a test for the empty case.") || !strings.Contains(got, "to post it: post "+heldRef) {
		t.Fatalf(":outbox:\n%s", got)
	}
	acted := func(command string) string {
		t.Helper()
		typed(m, command)
		pump(t, m, func() bool { return !m.working })
		return m.status
	}
	if status := acted(":workflows pause " + ref); !strings.Contains(status, "Check staging: Paused") {
		t.Fatalf("pause: %q", status)
	}
	if status := acted(":workflows resume " + ref); strings.Contains(status, "Paused") || !strings.Contains(status, "Check staging") {
		t.Fatalf("resume: %q", status)
	}
	if status := acted(":workflows pause nothing"); !strings.Contains(status, "no workflow ends with") {
		t.Fatalf("a reference that means nothing: %q", status)
	}
	if status := acted(":outbox discard " + heldRef); !strings.Contains(status, "Discarded. Nothing was posted.") {
		t.Fatalf("discard: %q", status)
	}
	state, err := store.Read(id)
	if err != nil || len(state.Outbox) != 0 || len(state.Delivered) != 1 || state.Delivered[0].Fate != "discarded" {
		t.Fatalf("%+v %v", state, err)
	}
	if got := view(":outbox"); !strings.Contains(got, "Nothing is waiting to be posted") {
		t.Fatalf(":outbox after a decision:\n%s", got)
	}
	typed(m, ":outbox frobnicate")
	if !strings.Contains(m.status, "usage: :workflows lists them") {
		t.Fatalf("%q", m.status)
	}
	if !strings.Contains(helpText, ":outbox post|discard REF") {
		t.Fatal("the help does not list the commands")
	}
}
