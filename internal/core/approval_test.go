package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func toolTurn(id, name string, args map[string]any) fake.Turn {
	return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: name, Args: args}}}
}

func shellTurn(id, cmd string) fake.Turn {
	return toolTurn(id, "shell-exec", map[string]any{"cmd": cmd, "args": []any{}})
}

func writeTurn(id, path, content string) fake.Turn {
	return toolTurn(id, "fs-write", map[string]any{"path": path, "content": content})
}

// asked sends text, answers every confirm with answer, and returns the
// prompts the person saw.
func (r *rig) asked(text, answer string) []event.Confirm {
	r.t.Helper()
	if err := r.s.Send(text); err != nil {
		r.t.Fatal(err)
	}
	var out []event.Confirm
	for _, ev := range r.drain(answer) {
		if ev.Kind == event.PromptConfirm {
			out = append(out, ev.Payload.(event.Confirm))
		}
	}
	return out
}

// decision is the guard's last recorded decision about prim on path ("" for
// any).
func (r *rig) decision(prim, path string) log.GuardDecision {
	r.t.Helper()
	var d log.GuardDecision
	for _, ev := range r.logged() {
		var g log.GuardDecision
		if ev.Kind == log.KindGuardDecision && ev.Decode(&g) == nil && g.Prim == prim && (path == "" || g.Path == path) {
			d = g
		}
	}
	return d
}

// By default a write the pin lets through goes ahead, standing, with its
// diff in the log (ADR 35); a protected path and credential-shaped content
// still ask the person.
func TestWritesInTheFolderGoAhead(t *testing.T) {
	t.Parallel()
	r := newRig(t, false,
		writeTurn("1", "notes/plan.md", "first step\n"),
		writeTurn("2", "Makefile", "all:\n"),
		writeTurn("3", "keys.txt", "sk-ant-abcdefgh1234"),
		fake.Turn{Content: "done"})
	asked := r.asked("write the plan", "n")
	if b, err := os.ReadFile(filepath.Join(r.repo, "notes", "plan.md")); err != nil || string(b) != "first step\n" {
		t.Fatalf("the write did not go ahead: %q %v", b, err)
	}
	d := r.decision("fs-write", "notes/plan.md")
	if d.Decision != "allow" || d.Checker != "standing" || !strings.Contains(d.Reason, "approval.writes") || !strings.Contains(d.Detail, "+first step") {
		t.Fatalf("the write's decision: %+v", d)
	}
	if len(asked) != 2 || asked[0].Prompt.Path != "Makefile" || !asked[0].Prompt.Security || !asked[1].Prompt.Security {
		t.Fatalf("want the protected path and the credential asked, got %+v", asked)
	}
	for _, f := range []string{"Makefile", "keys.txt"} {
		if _, err := os.Stat(filepath.Join(r.repo, f)); !os.IsNotExist(err) {
			t.Fatalf("%s was written after a no", f)
		}
	}
}

// Without a judge, a command is the person's to answer.
func TestCommandsAskWithoutAJudge(t *testing.T) {
	t.Parallel()
	r := newRig(t, false, shellTurn("1", "true"), fake.Turn{Content: "done"})
	asked := r.asked("run it", "y")
	if len(asked) != 1 || asked[0].Prompt.Prim != "shell-exec" {
		t.Fatalf("want one command prompt, got %+v", asked)
	}
	if d := r.decision("shell-exec", ""); d.Answer != "yes" || d.Actor == "judge" {
		t.Fatalf("decision: %+v", d)
	}
}

// With a judge, it answers a command first; when it refuses, the person is
// asked and told what it answered.
func TestJudgeAnswersCommandsThenThePerson(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		allow float64
		asks  bool
	}{{0.9, false}, {0.3, true}} {
		judge := newFakeJudge(t, noul(0.2), choice(tc.allow))
		r := judgedRigWith(t, judge, config.JudgeContentPlan, "", func(o *Options) { o.NoConfirm = false },
			shellTurn("1", "true"), fake.Turn{Content: "done"})
		asked := r.asked("run the check", "y")
		if (len(asked) == 1) != tc.asks || len(asked) > 1 {
			t.Fatalf("allow %.1f: prompts %+v", tc.allow, asked)
		}
		if tc.asks && !strings.Contains(strings.Join(asked[0].Prompt.Reasons, "; "), "the judge did not allow it") {
			t.Fatalf("the prompt does not say the judge refused: %+v", asked[0].Prompt)
		}
		d := r.decision("shell-exec", "")
		if d.Answer != "yes" || (d.Actor == "judge") == tc.asks {
			t.Fatalf("allow %.1f: decision %+v", tc.allow, d)
		}
		if got := strings.Join(judge.asked(), " "); got != "noul choice" {
			t.Fatalf("allow %.1f: judge asked %s", tc.allow, got)
		}
		if !strings.Contains(r.logb.String(), "judged approval: shell-exec") {
			t.Fatal("no note of the judged answer")
		}
	}
}

// A judge on an OpenAI server asks OpenAI's Decisions API with that
// server's key (ADR 46): the plan gate's yes/no question is a predicate and
// a command's is a choice, both at /v1/decisions with the server's key as a
// bearer token, and their answers decide as Jev's do. The jev-key stored
// beside it is not sent.
func TestTheJudgeAsksOpenAIWithItsServersKey(t *testing.T) {
	t.Parallel()
	judge := newFakeJudge(t, noul(0.2), choice(0.9))
	r := judgedRigWith(t, judge, config.JudgeContentPlan, "", func(o *Options) {
		o.NoConfirm = false
		if _, err := o.Keychain.Add("model-api-openai-com", []byte("sk-openai-judge-0002")); err != nil {
			t.Fatal(err)
		}
		o.Config.Servers = map[string]config.Server{"openai-api": {Provider: "openai", URL: judge.URL + "/v1", Credential: "model-api-openai-com"}}
		o.Config.Judge.Server = "openai-api"
		o.Bindings = binding.Table{Bind: []binding.Binding{{Class: "model-api-openai-com", Dest: "127.0.0.1", Positions: []string{"header:Authorization:Bearer"}, Methods: []string{"POST"}}}}
	}, shellTurn("1", "true"), fake.Turn{Content: "done"})
	if asked := r.asked("run the check", "y"); len(asked) != 0 {
		t.Fatalf("the person was asked: %+v", asked)
	}
	if d := r.decision("shell-exec", ""); d.Answer != "yes" || d.Actor != "judge" {
		t.Fatalf("decision %+v", d)
	}
	if got := strings.Join(judge.asked(), " "); got != "predicate choice" {
		t.Fatalf("judge asked %s", got)
	}
	for i, path := range judge.paths {
		if path != "/v1/decisions" || judge.auths[i] != "Bearer sk-openai-judge-0002" {
			t.Fatalf("request %d went to %s with %q", i, path, judge.auths[i])
		}
	}
	if !strings.Contains(judge.bodies[0], `"model":"gpt-6-luna"`) || !strings.Contains(judge.bodies[1], `"choices":[{"value":"allow"`) {
		t.Fatalf("bodies: %s", judge.bodies)
	}
}

// A write is approval.writes', so the judge is never asked about one: with
// writes that ask, the person answers it.
func TestJudgeIsNotAskedAboutWrites(t *testing.T) {
	t.Parallel()
	judge := newFakeJudge(t, noul(0.2))
	r := judgedRigWith(t, judge, config.JudgeContentPlan, "", func(o *Options) {
		o.NoConfirm = false
		o.Config.Approval.Writes = config.ApproveAsk
	}, writeTurn("1", "out.txt", "x\n"), fake.Turn{Content: "done"})
	if asked := r.asked("write it", "y"); len(asked) != 1 || asked[0].Prompt.Path != "out.txt" {
		t.Fatalf("prompts %+v", asked)
	}
	if got := strings.Join(judge.asked(), " "); got != "noul" {
		t.Fatalf("judge asked %s", got)
	}
}

// approval.commands = "allow" answers ordinary confirms yes, attributed to
// the setting; a security confirm still asks the person.
func TestCommandsAllowedBySetting(t *testing.T) {
	t.Parallel()
	r := newRigWith(t, false, func(c *config.Config) { c.Approval.Commands = config.ApproveAllow },
		shellTurn("1", "true"), writeTurn("2", ".nodwyn/init.scm", "(define x 1)\n"), fake.Turn{Content: "done"})
	asked := r.asked("run it, then configure", "n")
	if d := r.decision("shell-exec", ""); d.Answer != "yes" || d.Actor != "setting" {
		t.Fatalf("command decision: %+v", d)
	}
	if len(asked) != 1 || !asked[0].Prompt.Security {
		t.Fatalf("want only the protected write asked, got %+v", asked)
	}
}

// approval.commands = "ask" asks the person even with a judge set up.
func TestCommandsAskBySetting(t *testing.T) {
	t.Parallel()
	judge := newFakeJudge(t, noul(0.2))
	r := judgedRigWith(t, judge, config.JudgeContentPlan, "", func(o *Options) {
		o.NoConfirm = false
		o.Config.Approval.Commands = config.ApproveAsk
	}, shellTurn("1", "true"), fake.Turn{Content: "done"})
	if asked := r.asked("run it", "y"); len(asked) != 1 {
		t.Fatalf("prompts %+v", asked)
	}
	if got := strings.Join(judge.asked(), " "); got != "noul" {
		t.Fatalf("judge asked %s", got)
	}
}

// With "don't ask" on, a command is approved by the switch and the judge is
// not asked about it.
func TestDontAskSkipsTheJudge(t *testing.T) {
	t.Parallel()
	judge := newFakeJudge(t, noul(0.2))
	r := judgedRigWith(t, judge, config.JudgeContentPlan, "", func(o *Options) { o.NoConfirm = false },
		shellTurn("1", "true"), fake.Turn{Content: "done"})
	if err := r.s.SetApproveAll(true, "unix:uid=1"); err != nil {
		t.Fatal(err)
	}
	if asked := r.asked("run it", "n"); len(asked) != 0 {
		t.Fatalf("prompts %+v", asked)
	}
	if d := r.decision("shell-exec", ""); d.Answer != "yes" || d.Scope != "all" {
		t.Fatalf("decision: %+v", d)
	}
	if got := strings.Join(judge.asked(), " "); got != "noul" {
		t.Fatalf("judge asked %s", got)
	}
}

// outsideTurn reads a file outside the repository, which asks as a security
// confirm.
func outsideTurn(id string) fake.Turn {
	return toolTurn(id, "shell-exec", map[string]any{"cmd": "cat", "args": []any{"/opt/nodwyn-test-outside/notes"}})
}

// Who answers commands answers a security confirm about one too (ADR 35,
// amended 2026-10-30): a path outside the repository, from the tarro
// sessions 792638a8 and 34fd6421, where the person was asked about reads of
// the module cache whichever answer they had chosen. With the judge, then
// the person, the judge is asked first; alone, it decides; without asking,
// nobody is asked; and asking asks.
func TestCommandsAnswerSecurityConfirms(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode     string
		asked    int
		judged   string
		decision string
		actor    string
	}{
		{config.ApproveJudgeAuto, 0, "noul choice", "yes", "judge"},
		{config.ApproveJudge, 0, "noul choice", "yes", "judge"},
		{config.ApproveAllow, 0, "noul", "yes", "setting"},
		{config.ApproveAsk, 1, "noul", "no", ""},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			judge := newFakeJudge(t, noul(0.2), choice(0.95))
			r := judgedRigWith(t, judge, config.JudgeContentPlan, "", func(o *Options) {
				o.NoConfirm = false
				o.Config.Approval.Commands = tc.mode
			}, outsideTurn("1"), fake.Turn{Content: "done"})
			asked := r.asked("run it", "n")
			if len(asked) != tc.asked || (tc.asked == 1 && !asked[0].Prompt.Security) {
				t.Fatalf("prompts %+v", asked)
			}
			if got := strings.Join(judge.asked(), " "); got != tc.judged {
				t.Fatalf("judge asked %q, want %q", got, tc.judged)
			}
			if d := r.decision("shell-exec", ""); d.Answer != tc.decision || (tc.actor != "" && d.Actor != tc.actor) {
				t.Fatalf("decision: %+v", d)
			}
		})
	}
}

// With the judge, then the person, a security confirm the judge does not
// allow goes to the person, told what the judge answered.
func TestJudgeThenPersonAsksAboutARefusedSecurityConfirm(t *testing.T) {
	t.Parallel()
	judge := newFakeJudge(t, noul(0.2), choice(0.1))
	r := judgedRigWith(t, judge, config.JudgeContentPlan, "", func(o *Options) {
		o.NoConfirm = false
		o.Config.Approval.Commands = config.ApproveJudge
	}, outsideTurn("1"), fake.Turn{Content: "done"})
	asked := r.asked("run it", "y")
	if len(asked) != 1 || !asked[0].Prompt.Security || !strings.Contains(strings.Join(asked[0].Prompt.Reasons, "; "), "the judge did not allow it") {
		t.Fatalf("prompts %+v", asked)
	}
	if d := r.decision("shell-exec", ""); d.Answer != "yes" {
		t.Fatalf("decision: %+v", d)
	}
}

// A task's own choice of who answers its commands replaces the setting, from
// the next confirm once changed.
func TestATasksCommandsReplaceTheSetting(t *testing.T) {
	t.Parallel()
	r := judgedRigWith(t, nil, config.JudgeContentPlan, "", func(o *Options) {
		o.NoConfirm = false
		o.Config.Approval.Commands = config.ApproveAsk
		o.Commands = &CommandsChoice{Mode: config.ApproveAllow, Actor: "opener"}
	}, outsideTurn("1"), fake.Turn{Content: "done"})
	if mode, actor := r.s.Commands(); mode != config.ApproveAllow || actor != "opener" {
		t.Fatalf("commands %q by %q", mode, actor)
	}
	if asked := r.asked("run it", "n"); len(asked) != 0 {
		t.Fatalf("prompts %+v", asked)
	}
	if d := r.decision("shell-exec", ""); d.Answer != "yes" || d.Actor != "setting" {
		t.Fatalf("decision: %+v", d)
	}
	if err := r.s.SetCommands("sometimes", "desk"); err == nil {
		t.Fatal("a mode approval.commands does not take was taken")
	}
}

// Choosing who answers commands while a question about one waits for the
// person answers it by the new choice: the card goes, the command runs, and
// nobody answered it.
func TestChoosingCommandsAnswersTheQuestionWaiting(t *testing.T) {
	t.Parallel()
	r := judgedRigWith(t, nil, config.JudgeContentPlan, "", func(o *Options) {
		o.NoConfirm = false
		o.Config.Approval.Commands = config.ApproveAsk
	}, outsideTurn("1"), fake.Turn{Content: "done"})
	if err := r.s.Send("run it"); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(idleWait)
	asked, closed := "", ""
	for done := false; !done; {
		select {
		case ev := <-r.s.Events():
			switch ev.Kind {
			case event.PromptConfirm:
				asked = ev.Payload.(event.Confirm).ID
				if err := r.s.SetCommands(config.ApproveAllow, "desk"); err != nil {
					t.Fatal(err)
				}
			case event.PromptClosed:
				closed = ev.Payload.(event.Closed).ID
			case event.Status:
				done = ev.Payload.(event.StatusInfo).State == "idle"
			}
		case <-timeout:
			t.Fatal("no idle")
		}
	}
	if asked == "" || closed != asked {
		t.Fatalf("asked %q, closed %q", asked, closed)
	}
	if d := r.decision("shell-exec", ""); d.Answer != "yes" || d.Actor != "setting" || d.PromptID != asked {
		t.Fatalf("decision: %+v", d)
	}
}

// With "judge-auto" a judge that does not allow is a no, and the person is
// not asked; with no judge set up it is a no too.
func TestJudgeAutoRefusesWithoutAsking(t *testing.T) {
	t.Parallel()
	for name, judge := range map[string]*fakeJudge{"refusing": newFakeJudge(t, noul(0.2), choice(0.1)), "none": nil} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := judgedRigWith(t, judge, config.JudgeContentPlan, "", func(o *Options) {
				o.NoConfirm = false
				o.Config.Approval.Commands = config.ApproveJudgeAuto
			}, outsideTurn("1"), fake.Turn{Content: "done"})
			if asked := r.asked("run it", "y"); len(asked) != 0 {
				t.Fatalf("prompts %+v", asked)
			}
			if d := r.decision("shell-exec", ""); d.Answer != "no" {
				t.Fatalf("decision: %+v", d)
			}
		})
	}
}
