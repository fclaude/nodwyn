;; The full profile (ADR 32, amended 2026-10-17): the default loop as it was
;; before the lean ladder. The host evaluates this file after the default
;; loop and before .nodwyn/init.scm when the session's profile is "full"
;; (config.toml [loop] profile, NODWYN_PROFILE; "auto" picks it when a person
;; approves the session's calls), so a project's init.scm still has the last
;; word. The lean profile evaluates nothing: the defaults in plan.scm,
;; prompts.scm and strategy.scm are the lean ones.
;;
;; Each line turns off one switch the ladder turned on. A project that wants
;; part of the lean loop under the full profile, or the reverse, redefines
;; the same names in init.scm; docs/scheme.md §9.8 says what each does.

;; The system prompt is the interactive or the unattended one, and the control
;; model is offered every tool, not lean-tools.
(define lean-prompt #f)

;; A run's plan goes through the explorer, the planner and the judge, not the
;; one-line plan plan-minimal-text, and the model is shown the plan it has.
(define plan-minimal #f)
(define lean-plan-hidden #f)

;; The harness speaks to the model while it works and as it ends: the finish
;; review, the reminders, the question on an edited test, the hint on
;; failures the unchanged code has. finish-reminders, finish-review-limit and
;; lean-empty-reminders are computed from lean-quiet when strategy.scm loads,
;; so they are set again here: lean-quiet alone does not bring them back.
(define lean-quiet #f)
(define finish-reminders 2)
(define finish-review-limit 2)
(define lean-empty-reminders 0)

;; A shell-exec call that only reads (grep, sed -n, cat) does not count toward
;; the rounds in a row that only look.
(define lean-shell-look #f)

;; Twelve rounds in a row that only read get the reading reminder, and a
;; command that fails for a missing module gets the environment hint.
(define lean-no-read-reminder #f)
(define lean-no-envhint #f)
