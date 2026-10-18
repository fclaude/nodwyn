package workflows

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/workflow"
)

type fakeCaller struct {
	calls  []string
	params []any
	answer map[string]any
}

func (f *fakeCaller) Call(_ context.Context, method string, params, result any) error {
	f.calls = append(f.calls, method)
	f.params = append(f.params, params)
	if raw, ok := f.answer[method]; ok && result != nil {
		b, _ := json.Marshal(raw)
		return json.Unmarshal(b, result)
	}
	return nil
}

func sample() ([]rpc.WorkflowView, []rpc.OutboxView) {
	d := &workflow.Define{Definition: workflow.Definition{Name: "Review pull requests", DayCost: 5}}
	v := rpc.WorkflowView{State: workflow.State{ID: "20261018T030627Z-03a50c51a626", Definition: d, Outbox: []workflow.Outbox{{}}, LastSkip: &workflow.Skip{Reason: "Could not check the source: the keychain is locked"}}, Sentence: "When a pull request on fc/nodwyn is opened, checked every 2 minutes", Status: "Watching 3 items, checked Sat 17 Oct 14:02", Running: 1, Recent: []rpc.FireView{{When: "Sat 17 Oct 14:02", Fire: workflow.Fire{Summary: "Pull request #142 opened: fix: login loop"}}}}
	v.Today.Runs, v.Today.Cost = 2, 1.4
	held := rpc.OutboxView{Outbox: workflow.Outbox{ID: "20261018T030627Z-aaaa1111bbbb", Kind: "review", Item: "142", Task: "t-1", Summary: "Pull request #142 opened: fix: login loop", Body: "Needs a test.\nSecond.\n3\n4\n5\n6\n7\n8"}, Workflow: v.ID, Name: "Review pull requests", When: "Sat 17 Oct 20:07"}
	draft := rpc.OutboxView{Outbox: workflow.Outbox{ID: "20261018T030627Z-cccc2222dddd", Kind: "draft", Item: "t9", Summary: "New email from Bo: Invoice 88", Body: "Thanks, Bo.", Reply: nil}, Workflow: v.ID, Name: "Client mail", When: "Sat 17 Oct 20:08"}
	return []rpc.WorkflowView{v}, []rpc.OutboxView{held, draft}
}

// The words are the daemon's, laid out the same for every terminal client.
func TestWorkflowsAndHeldAnswersAreLaidOut(t *testing.T) {
	views, held := sample()
	out := Format(views)
	for _, want := range []string{"Review pull requests  [" + Ref(views[0].ID) + "]", "When a pull request on fc/nodwyn is opened", "Watching 3 items", "Today: 2 runs · $1.40 of $5.00 a day · 1 running", "1 answer waiting to be posted", "Note: Could not check the source", "Sat 17 Oct 14:02  Pull request #142 opened"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list has no %q:\n%s", want, out)
		}
	}
	text := FormatHeld(held)
	for _, want := range []string{"[" + Ref(held[0].ID) + "] Review for #142 · Review pull requests", "> Needs a test.", "(2 more lines; the whole answer is in the task t-1)", "to post it: post " + Ref(held[0].ID), "Draft reply · Client mail", "to save the draft it: post"} {
		if !strings.Contains(text, want) {
			t.Errorf("the held list has no %q:\n%s", want, text)
		}
	}
	if !strings.Contains(Format(nil), "No workflows") || !strings.Contains(FormatHeld(nil), "Nothing is waiting") {
		t.Error("empty lists are not said")
	}
}

func TestAReferenceMeansOneThing(t *testing.T) {
	views, held := sample()
	if v, err := FindWorkflow(views, Ref(views[0].ID)); err != nil || v.ID != views[0].ID {
		t.Fatalf("%v", err)
	}
	if _, err := FindWorkflow(views, "nothing"); err == nil {
		t.Error("a reference that matches nothing")
	}
	if _, err := FindWorkflow(views, ""); err == nil {
		t.Error("an empty reference")
	}
	two := append(held, held[0])
	if _, err := FindHeld(two, Ref(held[0].ID)); err == nil || !strings.Contains(err.Error(), "2 held answers") {
		t.Errorf("an ambiguous reference: %v", err)
	}
	if i, err := FindHeld(held, Ref(held[1].ID)); err != nil || i.Kind != "draft" {
		t.Fatalf("%+v %v", i, err)
	}
}

// Acting sends what the console sends, and says what was done: a post or a
// discard of a held answer, never other words than the run's.
func TestActingSendsTheConsolesRequests(t *testing.T) {
	views, held := sample()
	c := &fakeCaller{answer: map[string]any{"workflow.pause": rpc.WorkflowView{Status: "Paused"}, "workflow.check": rpc.WorkflowView{Status: "Watching 4 items"}}}
	if msg, err := Act(context.Background(), c, views[0], "pause"); err != nil || msg != "Review pull requests: Paused" {
		t.Fatalf("%q %v", msg, err)
	}
	if msg, err := Act(context.Background(), c, views[0], "check"); err != nil || !strings.Contains(msg, "Watching 4 items") {
		t.Fatalf("%q %v", msg, err)
	}
	if _, err := Act(context.Background(), c, views[0], "delete"); err == nil {
		t.Error("an action the daemon has no method for")
	}
	if msg, err := Decide(context.Background(), c, held[0], "post"); err != nil || msg != "Posted." {
		t.Fatalf("%q %v", msg, err)
	}
	if msg, err := Decide(context.Background(), c, held[1], "post"); err != nil || !strings.Contains(msg, "Nothing was sent") {
		t.Fatalf("%q %v", msg, err)
	}
	if msg, err := Decide(context.Background(), c, held[1], "discard"); err != nil || msg != "Discarded. Nothing was saved." {
		t.Fatalf("%q %v", msg, err)
	}
	last := c.params[len(c.params)-1].(map[string]any)
	if last["workflow"] != views[0].ID || last["decision"] != "discard" || last["id"] != held[1].ID {
		t.Fatalf("%+v", last)
	}
	if _, has := last["body"]; has {
		t.Fatal("a terminal client sent words of its own")
	}
	if strings.Join(c.calls, ",") != "workflow.pause,workflow.check,outbox.decide,outbox.decide,outbox.decide" {
		t.Fatal(c.calls)
	}
}

func timetableAnswer() map[string]any {
	return map[string]any{"session.list": []map[string]any{
		{"id": "20261018T010101Z-aaaabbbb1111", "title": "Release notes", "archived": false, "schedule": map[string]any{
			"definition": map[string]any{"message": "Draft them.", "paused": false, "day_cost": 3.0, "actor": "x"},
			"sentence":   "Tuesdays and Thursdays at 15:00", "next_words": []string{"Tue 20 Oct 15:00", "Thu 22 Oct 15:00"},
			"today": map[string]any{"day": "2026-10-20", "runs": 1, "cost": 0.31}, "history": []map[string]any{{"what": "Finished (Run now)"}},
		}},
		{"id": "20261018T010101Z-ccccdddd2222", "title": "Archived one", "archived": true, "schedule": map[string]any{"definition": map[string]any{"message": "x"}}},
		{"id": "20261018T010101Z-eeeeffff3333", "title": "No schedule"},
	}, "workflow.list": []rpc.WorkflowView{}, "outbox.list": []rpc.OutboxView{}}
}

// The timetables (scheduled tasks) are listed with the workflows that watch a
// source, as the console's page does, and acted on by the same references.
func TestTimetablesAreListedAndActedOnLikeWorkflows(t *testing.T) {
	c := &fakeCaller{answer: timetableAnswer()}
	l, err := Read(context.Background(), c)
	if err != nil || len(l.Timetables) != 1 {
		t.Fatalf("%+v %v", l, err)
	}
	out := l.Workflows()
	for _, want := range []string{"Release notes  [bbbb1111]  a timetable", "Tuesdays and Thursdays at 15:00", "Next: Tue 20 Oct 15:00", "Today: 1 run · $0.310 of $3.00 a day · Last: Finished (Run now)"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list has no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Archived one") || strings.Contains(out, "No schedule") {
		t.Errorf("a task without a schedule, or an archived one, is listed:\n%s", out)
	}
	if msg, err := l.Act(context.Background(), c, "aaaabbbb1111", "pause"); err != nil || msg != "Release notes: paused." {
		t.Fatalf("%q %v", msg, err)
	}
	if msg, err := l.Act(context.Background(), c, "bbbb1111", "run"); err != nil || msg != "Release notes: a run started." {
		t.Fatalf("%q %v", msg, err)
	}
	if _, err := l.Act(context.Background(), c, "aaaabbbb1111", "check"); err == nil {
		t.Error("a timetable was checked")
	}
	if _, err := l.Act(context.Background(), c, "nothing", "pause"); err == nil {
		t.Error("a reference that means nothing")
	}
	got := c.params[len(c.params)-2:]
	if got[0].(map[string]any)["paused"] != true || got[1].(map[string]any)["id"] != "20261018T010101Z-aaaabbbb1111" {
		t.Fatalf("%+v", got)
	}
	if strings.Join(c.calls[len(c.calls)-2:], ",") != "schedule.pause,schedule.run" {
		t.Fatal(c.calls)
	}
}
