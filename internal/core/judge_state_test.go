package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func commandTurn(id, cmd string, args ...string) fake.Turn {
	list := make([]any, len(args))
	for i, a := range args {
		list[i] = a
	}
	return toolTurn(id, "shell-exec", map[string]any{"cmd": cmd, "args": list})
}

// The judge is told whether a person is at hand: in a session they can
// answer, a call it does not allow is put to them, and its question says so;
// in a run nobody watches it is told that. Neither is told a command is
// irreversible, which every command is (session 20261005T054223Z-36b27caa
// sent four runs of the project's own tests to the person).
func TestTheJudgeKnowsWhetherAPersonIsAtHand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		adjust func(*Options)
		want   string
		answer string
	}{
		{"a session a person answers", func(o *Options) { o.NoConfirm = false }, "for a person who is at hand: a call you do not allow is put to them instead", "safe to make without asking the person"},
		{"a run nobody watches", judged, "without a person watching", "safe to make without a person"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			judge := newFakeJudge(t, noul(0.2), choice(0.9))
			r := judgedRigWith(t, judge, config.JevContentPlan, quiet, tc.adjust, commandTurn("1", "true"), fake.Turn{Content: "done"})
			if asked := r.asked("run the check", "y"); len(asked) != 0 {
				t.Fatalf("the person was asked: %+v", asked)
			}
			if len(judge.bodies) != 2 || !strings.Contains(judge.bodies[1], tc.want) || !strings.Contains(judge.bodies[1], tc.answer) {
				t.Fatalf("the judge was asked:\n%s", strings.Join(judge.bodies, "\n"))
			}
			if strings.Contains(judge.states[1], "irreversible") {
				t.Fatalf("the judge was told the command is irreversible:\n%s", judge.states[1])
			}
		})
	}
}

// The judge sees the plan with the steps marked so far and the next one, and
// what was answered about the same call earlier in the turn: here, that it
// did not allow it and the person did.
func TestTheJudgeSeesPlanProgressAndEarlierAnswers(t *testing.T) {
	t.Parallel()
	judge := newFakeJudge(t, noul(0.9), score(3.6), choice(0.3), choice(0.9))
	r := judgedRigWith(t, judge, config.JevContentPlan, quiet, func(o *Options) { o.NoConfirm = false },
		fake.Turn{Content: "1. run the check\n2. run it again\n3. report"},
		commandTurn("a", "echo", "check"),
		fake.Turn{ToolCalls: []fake.ToolCall{stepCall("b", 1, "done", "the check ran")}},
		commandTurn("c", "echo", "check"),
		fake.Turn{Content: "done"})
	asked := r.asked("run the check twice", "y")
	if len(asked) != 1 {
		t.Fatalf("the person was asked %d times: %+v", len(asked), asked)
	}
	if got := strings.Join(judge.asked(), " "); got != "noul score choice choice" {
		t.Fatalf("judge asked %s", got)
	}
	first, second := judge.states[2], judge.states[3]
	if strings.Contains(first, "earlier in this turn") || strings.Contains(first, "[done") {
		t.Fatalf("the first ask had history:\n%s", first)
	}
	for _, want := range []string{
		"1. [done: the check ran] run the check",
		"2. [next] run it again",
		"3. [to do] report",
		"This same call earlier in this turn: not allowed by you, at a probability of allow of 0.30; then allowed by the person.",
	} {
		if !strings.Contains(second, want) {
			t.Fatalf("the second ask does not say %q:\n%s", want, second)
		}
	}
}

// A command that runs a script from the session's own scratch directory
// tells the judge the script is the session's; at "full", where file
// contents may leave (ADR 19), it also shows the script's text. A file the
// command names outside the scratch directory, or that is not there, is not
// described.
func TestTheJudgeIsToldOfAScratchScript(t *testing.T) {
	t.Parallel()
	for _, content := range []string{config.JevContentPlan, config.JevContentFull} {
		t.Run(content, func(t *testing.T) {
			t.Parallel()
			judge := newFakeJudge(t, noul(0.2), choice(0.9))
			r := judgedRigWith(t, judge, content, quiet, func(o *Options) { o.NoConfirm = false },
				commandTurn("1", "bash", "-c", `bash "$TMPDIR/check.sh" tests; cat "$TMPDIR/absent.sh" /etc/hostname`), fake.Turn{Content: "done"})
			if err := os.WriteFile(filepath.Join(r.s.scratch, "check.sh"), []byte("# script-marker-7731\necho checking\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if asked := r.asked("run the check", "y"); len(asked) != 0 {
				t.Fatalf("the person was asked: %+v", asked)
			}
			state := judge.states[1]
			if !strings.Contains(state, `It runs $TMPDIR/check.sh: a 2-line file in this session's own scratch directory`) {
				t.Fatalf("the judge was not told of the script:\n%s", state)
			}
			if strings.Contains(state, "absent.sh: a") || strings.Contains(state, "hostname: a") {
				t.Fatalf("a file not in the scratch directory was described:\n%s", state)
			}
			if shown := strings.Contains(state, "script-marker-7731"); shown != (content == config.JevContentFull) {
				t.Fatalf("at %s the script's text shown = %v:\n%s", content, shown, state)
			}
		})
	}
}

// A chat's later message is read after the person's messages before it:
// by the plan gate's judge, its planner and its scoring, and by the judge
// of a command; the planner also reads the agent's last answer. In session 20261005T060224Z-2f9eb922 the reply below,
// shown alone, was put at 0.4 for a plan, and a look at the tools it would
// need at 0.41 for a command.
func TestTheJudgeReadsAChatsEarlierMessages(t *testing.T) {
	t.Parallel()
	const first, reply = "that is still verbose, compacting should be possible there", "folding, to reduce visual polution for the user"
	judge := newFakeJudge(t, noul(0.2), noul(0.9), score(3.6), choice(0.9))
	r := judgedRigWith(t, judge, config.JevContentPlan, quiet, func(o *Options) { o.NoConfirm = false },
		fake.Turn{Content: "Do you mean folding the activity rows, or compacting the context?"},
		fake.Turn{Content: "1. Change: transcript.js — fold the activity rows."},
		commandTurn("1", "date", "-I"),
		fake.Turn{Content: "done"})
	if asked := r.asked(first, "y"); len(asked) != 0 {
		t.Fatalf("the person was asked: %+v", asked)
	}
	if asked := r.asked(reply, "y"); len(asked) != 0 {
		t.Fatalf("the person was asked: %+v", asked)
	}
	if got := strings.Join(judge.asked(), " "); got != "noul noul score choice" {
		t.Fatalf("judge asked %s", got)
	}
	if judge.states[0] != first {
		t.Fatalf("the first message was not read alone:\n%s", judge.states[0])
	}
	read := "Earlier in this conversation the person wrote:\n" + first + "\n\n"
	for i, want := range []string{
		read + "Their latest message, the request to carry out now:\n" + reply,
		read + "Their latest message, the request to carry out now:\n" + reply,
		read + "Task:\n" + reply + "\n",
	} {
		if !strings.Contains(judge.states[i+1], want) {
			t.Fatalf("the judge's %s was not told %q:\n%s", judge.questions[i+1], want, judge.states[i+1])
		}
	}
	if planner := string(r.srv.Requests[1].Raw); !strings.Contains(planner, "You are nodwyn's planner") || !strings.Contains(planner, `the person wrote:\n`+first) {
		t.Fatalf("the planner did not read the earlier message:\n%s", planner)
	}
	// The planner also reads the agent's last answer, which the reply
	// answers; the judge is never sent it (ADR 19).
	if planner := string(r.srv.Requests[1].Raw); !strings.Contains(planner, `The agent last answered:\nDo you mean folding the activity rows`) {
		t.Fatalf("the planner did not read the agent's last answer:\n%s", planner)
	}
	for i, state := range judge.states {
		if strings.Contains(state, "Do you mean folding") {
			t.Fatalf("the judge's %s was sent the agent's answer:\n%s", judge.questions[i], state)
		}
	}
	if !strings.Contains(judge.bodies[1], "asks for an answer rather than a change") || !strings.Contains(judge.bodies[3], "a command that only looks or reports") {
		t.Fatalf("the judge was asked:\n%s\n%s", judge.bodies[1], judge.bodies[3])
	}
}
