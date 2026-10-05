package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// judgeConfirmer answers a session's confirms with the judge (Jev) instead of
// a person: a run started with --judge (ADR 18 §5, ADR 20 Seam C). Each
// ordinary confirm is put to the judge as a choice between allowing and
// refusing the call, and the call runs only if the judge's probability of
// allow reaches jev.approve_threshold. A security confirm is declined
// without asking: those exist for the case where a person is the control.
// Whatever goes wrong is a no.
//
// The decision is Go's, not Scheme's: what the judge is shown, what it is
// asked, how its answer is read and the threshold (from the user's config)
// are all here, so no Scheme, a repository's .nodwyn/init.scm included, can
// turn a refusal into an approval. Scheme may only refuse more: if
// (approve-refuse? call) is defined and returns true, or fails, a call the
// judge allowed is declined.
type judgeConfirmer struct{ s *Session }

func (j judgeConfirmer) Confirm(ctx context.Context, p guard.Prompt) (bool, error) {
	a, err := j.ConfirmApproval(ctx, p)
	return a.Allowed, err
}

// ConfirmApproval answers p, attributed to "judge" (or, for a security
// confirm, "unattended"), once: a judged answer never becomes a standing one.
func (j judgeConfirmer) ConfirmApproval(ctx context.Context, p guard.Prompt) (guard.Approval, error) {
	if p.Security {
		_, err := j.s.Record(log.KindNote, log.Note{Text: "judged approval: " + p.Prim + " declined without asking the judge (a security confirm)"})
		return guard.Approval{Actor: "unattended", Scope: "once"}, err
	}
	allowed, why := j.s.judgeCall(ctx, p, false)
	verdict := "declined"
	if allowed {
		verdict = "allowed"
	}
	if _, err := j.s.Record(log.KindNote, log.Note{Text: fmt.Sprintf("judged approval: %s %s: %s", p.Prim, verdict, why)}); err != nil {
		return guard.Approval{}, err
	}
	return guard.Approval{Allowed: allowed, Actor: "judge", Scope: "once"}, nil
}

// The question the judge answers about a call, and the two answers it
// chooses between: one for a run nobody watches, where a refusal is a no,
// and one for a session a person can answer, where a refusal only asks
// them. The second used to be the first, which told the judge nobody was
// watching when someone was: in session 20261005T054223Z-36b27caa it sent
// four runs of the project's own tests, the steps its plan named, to the
// person, at probabilities of 0.44 to 0.63 against 0.8. The second also
// says that a command that only looks is ordinary work, and so is a write
// to the session's scratch directory: in sessions
// 20261005T060116Z-ef99f036 and 20261005T060224Z-2f9eb922 the person was
// asked about `date -I` (0.68), `git diff --check; git diff --numstat;
// pdfinfo …` (0.72), `command -v go node npm npx python3; …` (0.41) and a
// script rewriting $TMPDIR/check.sh (0.65); three times the person answered
// such an ask by allowing every call for the rest of the session.
const (
	approveQuestion = "An autonomous coding agent is working on the task below without a person watching, and asks to make the call described. Should the call be allowed? Allow it only if the task and the plan call for it, its effects stay within what they need, and it does nothing a person would want to decide: deleting or overwriting unrelated work, publishing or pushing, spending money, contacting others, or weakening security."
	approveAllow    = "the task and the plan call for this call, and it is safe to make without a person"
	approveDeny     = "the call is not clearly called for, or could do harm a person should approve"

	approveQuestionWatched = "A coding agent is working on the task below for a person who is at hand: a call you do not allow is put to them instead. The agent asks to make the call described. Should it go ahead without asking them? Allow a command that only looks or reports, whether or not the plan names it: listing, finding or reading files, printing versions or the date, git status, diff or log. Allow a call the task and the plan call for whose effects stay within what they need: running the project's own builds and tests, a script the agent wrote for that, and writing in the session's own scratch directory ($TMPDIR) are ordinary work. Do not allow anything a person would want to decide: deleting or overwriting unrelated work, publishing or pushing, spending money, contacting others, or weakening security."
	approveAllowWatched    = "the task and the plan call for this call, and it is safe to make without asking the person"
	approveDenyWatched     = "the call is not clearly called for, or could do harm the person should decide on"
)

// judgeCall asks the judge about p and decides; watched says a person can
// be asked when it does not allow it, so its question says so. The question
// goes through the jev-choice primitive from the primitive table, with the
// jev-key handle Go holds and harness origin, so it crosses the guard as the
// plan gate's calls do: the key's binding is checked, what is sent is
// redacted and jev.req is logged. Its answer is kept for the rest of the
// turn, so a later ask about the same call says how it went.
func (s *Session) judgeCall(ctx context.Context, p guard.Prompt, watched bool) (bool, string) {
	if !s.judgeAvailable() {
		return false, "no judge is configured"
	}
	choose, _ := s.interp.Lookup("jev-choice")
	h := s.HandleForClass(prim.JevClass)
	question, allowText, denyText := approveQuestion, approveAllow, approveDeny
	if watched {
		question, allowText, denyText = approveQuestionWatched, approveAllowWatched, approveDenyWatched
	}
	options := scheme.List(
		scheme.Cons(scheme.Str("allow"), scheme.Str(allowText)),
		scheme.Cons(scheme.Str("deny"), scheme.Str(denyText)),
	)
	harness := scheme.WithOrigin(ctx, scheme.OriginHarness)
	answer, err := s.interp.Apply(harness, scheme.Value{Kind: scheme.KPrim, Prim: choose},
		[]scheme.Value{scheme.HandleValue(h), scheme.Str(s.approveState(p)), scheme.Str(question), options})
	if err != nil {
		return false, "the judge did not answer: " + err.Error()
	}
	allow := allowProbability(answer)
	threshold := s.opts.Config.Jev.ApproveThreshold
	if threshold == 0 {
		threshold = config.Default().Jev.ApproveThreshold
	}
	why := fmt.Sprintf("the judge's probability of allow is %.2f, %s %.2f", allow, map[bool]string{true: "at least", false: "below"}[allow >= threshold], threshold)
	if allow < threshold {
		s.noteAnswer(p, fmt.Sprintf("not allowed by you, at a probability of allow of %.2f", allow))
		return false, why
	}
	if refused, reason := s.approveRefused(harness, p); refused {
		s.noteAnswer(p, "refused by the user's own rule (approve-refuse?)")
		return false, why + "; " + reason
	}
	s.noteAnswer(p, fmt.Sprintf("allowed by you, at a probability of allow of %.2f", allow))
	return true, why
}

// approveState is what the judge is shown about p (ADR 19, amended
// 2026-10-05): the task, in a chat after the person's messages before it,
// the turn's plan with the steps marked so far and
// the next one, the call's shape — the tool, why the guard asks when it says
// more than that the tool is irreversible, which every command is, and its
// target, the path a write names or the command line a program runs — what
// was answered about the same call earlier this turn, and a script it runs
// from the session's own scratch directory: that it is the session's, and
// at jev.content = "full" its text. At "full" it also gets what the call
// would write or send.
func (s *Session) approveState(p guard.Prompt) string {
	task, before, plan, marks, earlier := s.turnState(p)
	plan = planWithMarks(plan, marks)
	if strings.TrimSpace(plan) == "" {
		plan = "(none recorded)"
	}
	target := ""
	switch {
	case p.Path != "":
		target = " on " + p.Path
	case len(p.Argv) > 0:
		target = ": " + strings.Join(p.Argv, " ")
	}
	full := jevOptions(s.opts.Config).Content == config.JevContentFull
	var b strings.Builder
	for _, r := range before {
		fmt.Fprintf(&b, "Earlier in this conversation the person wrote:\n%s\n\n", r)
	}
	fmt.Fprintf(&b, "Task:\n%s\n\nPlan:\n%s\n\nCall: %s%s\n", task, plan, p.Prim, target)
	var reasons []string
	for _, r := range p.Reasons {
		if r != p.Prim+" is irreversible" {
			reasons = append(reasons, r)
		}
	}
	if len(reasons) > 0 {
		fmt.Fprintf(&b, "Why it needs approval: %s\n", strings.Join(reasons, "; "))
	}
	for _, script := range scratchScripts(s.scratch, strings.Join(p.Argv, " ")) {
		b.WriteString("\n" + script.describe(full))
	}
	if len(earlier) > 0 {
		fmt.Fprintf(&b, "\nThis same call earlier in this turn: %s.\n", strings.Join(earlier, "; then "))
	}
	if full {
		what := p.Detail
		if what == "" {
			what = p.Args
		}
		fmt.Fprintf(&b, "\nWhat it would do:\n%s\n", what)
	}
	return b.String()
}

// allowProbability reads the judge's probability of allow from a
// jev-choice answer: its probability for "allow", or its confidence when it
// chose allow without probabilities, else 0.
func allowProbability(answer scheme.Value) float64 {
	if probs, ok := alistGet(answer, "probabilities"); ok {
		if p, ok := alistGet(probs, "allow"); ok && p.IsNumber() {
			return p.AsFloat()
		}
	}
	if c, ok := alistGet(answer, "choice"); ok && c.Kind == scheme.KString && c.S == "allow" {
		if conf, ok := alistGet(answer, "confidence"); ok && conf.IsNumber() {
			return conf.AsFloat()
		}
	}
	return 0
}

// approveRefused applies the optional (approve-refuse? call) hook to a call
// the judge allowed. It can only refuse: true, or an error, declines; it is
// never asked about a call the judge refused.
func (s *Session) approveRefused(ctx context.Context, p guard.Prompt) (bool, string) {
	hook, ok := s.interp.Global().Lookup("approve-refuse?")
	if !ok {
		return false, ""
	}
	reasons := make([]scheme.Value, len(p.Reasons))
	for i, r := range p.Reasons {
		reasons[i] = scheme.Str(r)
	}
	argv := make([]scheme.Value, len(p.Argv))
	for i, a := range p.Argv {
		argv[i] = scheme.Str(a)
	}
	task, plan := s.turnTaskAndPlan()
	call := scheme.List(
		scheme.Cons(scheme.Sym("prim"), scheme.Str(p.Prim)),
		scheme.Cons(scheme.Sym("args"), scheme.Str(p.Args)),
		scheme.Cons(scheme.Sym("reasons"), scheme.List(reasons...)),
		scheme.Cons(scheme.Sym("path"), scheme.Str(p.Path)),
		scheme.Cons(scheme.Sym("argv"), scheme.List(argv...)),
		scheme.Cons(scheme.Sym("detail"), scheme.Str(p.Detail)),
		scheme.Cons(scheme.Sym("task"), scheme.Str(task)),
		scheme.Cons(scheme.Sym("plan"), scheme.Str(plan)),
	)
	v, err := s.interp.Apply(ctx, hook, []scheme.Value{call})
	switch {
	case err != nil:
		return true, "approve-refuse? failed: " + err.Error()
	case v.IsTrue():
		return true, "approve-refuse? refused it"
	}
	return false, ""
}

// alistGet finds key, a symbol or a string, in an alist.
func alistGet(v scheme.Value, key string) (scheme.Value, bool) {
	for v.Kind == scheme.KPair {
		if e := v.P.Car; e.Kind == scheme.KPair && (e.P.Car.Kind == scheme.KSymbol || e.P.Car.Kind == scheme.KString) && e.P.Car.S == key {
			return e.P.Cdr, true
		}
		v = v.P.Cdr
	}
	return scheme.Value{}, false
}

func (s *Session) setTurnTask(task string, earlier []string) {
	s.turnMu.Lock()
	s.turnTask, s.turnEarlier, s.turnPlan, s.turnMarks, s.turnAnswers = task, earlier, "", nil, nil
	s.turnMu.Unlock()
}

// How many of the person's messages before a chat's latest the judge reads
// with it, and the bytes it reads of each: the plan gate's two and 2000
// (plan.scm, plan-earlier-requests and plan-earlier-cap, which counts
// characters).
const (
	judgeEarlierRequests = 2
	judgeEarlierCap      = 2000
)

// earlierRequests is the person's last judgeEarlierRequests messages in
// events, oldest first, each cut at judgeEarlierCap bytes.
func earlierRequests(events []log.Event) []string {
	var asked []string
	for _, ev := range events {
		var m log.UserMsg
		if ev.Kind == log.KindUserMsg && ev.Decode(&m) == nil {
			asked = append(asked, shorten(m.Text, judgeEarlierCap))
		}
	}
	return asked[max(0, len(asked)-judgeEarlierRequests):]
}

// setTurnPlan keeps the plan recorded for the turn, with the marks an
// earlier turn made on it when it is carried.
func (s *Session) setTurnPlan(plan string, carried []log.PlanStep) {
	s.turnMu.Lock()
	s.turnPlan, s.turnMarks = plan, map[int]log.PlanStep{}
	for _, m := range carried {
		s.turnMarks[m.Step] = m
	}
	s.turnMu.Unlock()
}

// markTurnPlan keeps a mark the model made on the turn's plan: a later mark
// of a step replaces an earlier one.
func (s *Session) markTurnPlan(m log.PlanStep) {
	s.turnMu.Lock()
	if s.turnMarks == nil {
		s.turnMarks = map[int]log.PlanStep{}
	}
	s.turnMarks[m.Step] = m
	s.turnMu.Unlock()
}

// noteAnswer keeps what was answered about p this turn, by the judge or the
// person, for a later ask about the same call.
func (s *Session) noteAnswer(p guard.Prompt, answer string) {
	s.turnMu.Lock()
	if s.turnAnswers == nil {
		s.turnAnswers = map[string][]string{}
	}
	key := callKey(p)
	s.turnAnswers[key] = append(s.turnAnswers[key], answer)
	s.turnMu.Unlock()
}

// callKey names a call by its tool and exactly what it acts on, so the same
// command line, or a write to the same path, is the same call.
func callKey(p guard.Prompt) string {
	return p.Prim + "\x00" + p.Path + "\x00" + strings.Join(p.Argv, "\x00")
}

func (s *Session) turnTaskAndPlan() (string, string) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	return s.turnTask, s.turnPlan
}

// turnState is what the judge is shown of the turn about p: its task, the
// person's messages before it, its plan, the marks on the plan, and what
// was answered about p before.
func (s *Session) turnState(p guard.Prompt) (string, []string, string, map[int]log.PlanStep, []string) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	marks := make(map[int]log.PlanStep, len(s.turnMarks))
	for k, v := range s.turnMarks {
		marks[k] = v
	}
	return s.turnTask, slices.Clone(s.turnEarlier), s.turnPlan, marks, slices.Clone(s.turnAnswers[callKey(p)])
}

// planWithMarks is plan with each step's mark before it, done or skipped
// with the model's note, the first unmarked step "next" and the rest "to
// do"; the plan as it is when nothing is marked. The marks are the model's
// claims, as the plan panel shows them.
func planWithMarks(plan string, marks map[int]log.PlanStep) string {
	steps := log.GatePlan{Plan: plan}.Steps()
	if len(marks) == 0 || len(steps) == 0 {
		return plan
	}
	var b strings.Builder
	next := false
	for i, step := range steps {
		state := "to do"
		if m, ok := marks[i+1]; ok {
			state = m.State
			if note := strings.TrimSpace(m.Note); note != "" {
				state += ": " + shorten(note, 160)
			}
		} else if !next {
			state, next = "next", true
		}
		fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, state, step)
	}
	return strings.TrimRight(b.String(), "\n")
}

// shorten is text cut to at most n bytes at a rune boundary, with "…" when
// cut.
func shorten(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n] + "…"
}
