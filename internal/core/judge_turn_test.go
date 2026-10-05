package core

import (
	"testing"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
)

// What the judge is told of a turn, the marks on its plan and the answers
// about its calls, starts empty with each turn: an answer is never carried
// into the next one, where it would read as a standing approval.
func TestTheJudgesTurnStateStartsEmpty(t *testing.T) {
	s := &Session{}
	s.setTurnTask("first", []string{"before"})
	s.setTurnPlan("1. one\n2. two", []log.PlanStep{{Step: 1, Of: 2, State: "done"}})
	s.markTurnPlan(log.PlanStep{Step: 2, Of: 2, State: "skipped", Note: "not needed"})
	call := guard.Prompt{Prim: "shell-exec", Argv: []string{"go", "test", "./..."}}
	s.noteAnswer(call, "allowed by the person")
	if _, _, _, marks, earlier := s.turnState(call); len(marks) != 2 || len(earlier) != 1 {
		t.Fatalf("marks %v, answers %v", marks, earlier)
	}
	if _, _, _, _, earlier := s.turnState(guard.Prompt{Prim: "shell-exec", Argv: []string{"go", "test", "./internal/..."}}); len(earlier) != 0 {
		t.Fatalf("another command line took this one's answers: %v", earlier)
	}
	s.setTurnTask("second", nil)
	if task, before, plan, marks, earlier := s.turnState(call); task != "second" || len(before) != 0 || plan != "" || len(marks) != 0 || len(earlier) != 0 {
		t.Fatalf("a new turn kept %q %v %q %v %v", task, before, plan, marks, earlier)
	}
}
