;; Loaded after the prelude when nodwyn starts in this repository (ADR 14).
;; It registers tools and describes them, and wraps the agent turn with the
;; finish check below; nothing here performs an action when it loads.
(load "dev/go.scm")
(load "dev/git.scm")
(load "dev/repo.scm")

;; These tools only look at the repository: rounds of nothing else count
;; toward the default loop's reading reminder (strategy.scm).
(define look-tools (append look-tools '("grep-repo" "find-files" "read-adr" "git-log")))

;; validate: the repository's check before work is called done (ADR 37),
;; scripts/validate behind make validate. It is defined here and not in
;; dev/, which takes writes without asking: a session must not be able to
;; change the check it is judged by. It runs in the background through
;; shell-start, since make accept and the browser checks together can pass
;; shell-exec's ten minutes, and reads until make exits.

;; The repository as the finish check compares it: the files git does not
;; track, and the diff of the tracked ones against the change's base, so a
;; second edit to a file already changed counts too and a commit changes
;; nothing. The base is the commit a session's worktree started from, else
;; the one HEAD named when the turn began (validation-base): against HEAD,
;; a commit emptied the diff, and every commit-only turn was told to
;; validate again (at least ten times in the local sessions of 2026-10-04
;; and 05:
;; 20261005T035352Z-5a76f6e4 seq 556, cb8a1bdf 482, 871dfe24 575, ...). An
;; untracked file edited again keeps its status line, and is missed.
(define validation-base (guard (e (else #f)) validation-base))

(define (validation-against)
  (or (guard (e (else #f)) (let ((w (worktree))) (and w (assoc-ref w "base-commit" #f))))
      validation-base
      "HEAD"))

(define (validation-state)
  (list (filter (lambda (l) (string-prefix? "?? " l)) (or (git-state) '()))
        (guard (e (else "")) (format "~a" (git-diff "." #f (validation-against))))))

;; What the repository looked like when validate last finished; kept across
;; a reload.
(define validated-state (guard (e (else #f)) validated-state))

(define (validate-read id)
  (let loop ((out ""))
    (let* ((text (format "~a" (shell-read id 60)))
           (lines (string-split text "\n"))
           (head (car lines))
           (body (string-join (cdr lines) "\n")))
      (if (and (string-contains? head ": exited ")
               (not (string-contains? text "more bytes: read again]")))
          (string-append head "\n" out body)
          (loop (string-append out body))))))

(register-tool "validate"
  (lambda ()
    (let* ((started (format "~a" (shell-start "make" '("validate"))))
           (lines (string-split started "\n"))
           (id (car (string-split (car lines) ":")))
           (opening (string-join (cdr lines) "\n"))
           (report (validate-read id)))
      (set! validated-state (validation-state))
      (string-append opening report)))
  '((doc . "Run this repository's checks for what changed since the branch left its upstream (make validate, scripts/validate): gofmt, make accept, the browser checks for console changes, the book and expert guide for their sources, each changed Go test against the unchanged code, and the house rules a diff can show. It can take ten minutes or more. Returns a summary whose last line says PASS or FAIL; quote it in your answer, warnings included.")
    (params . ())))

;; The finish check. A turn that changed files and ends without validate
;; having run on the repository as it now is gets told so, at most
;; validate-reminders times, and goes on. The default loop's own finish
;; check runs only in unattended runs and planned turns (strategy.scm), so
;; a chat here had none: on 2026-10-03 a chat session called a console
;; feature done that it had never run. A turn that changed nothing, such as
;; one that answers a question, is not reminded.
(define validate-reminders 2)

(define validate-reminder
  "You changed files in this turn and validate has not run since your last change. Run validate, read its summary, fix what it reports, and quote the summary in your answer, warnings included.")

;; The turn this wraps, captured once: a reload runs this file again, and
;; would otherwise wrap the wrapper.
(define validate-base-agent-turn
  (or (guard (e (else #f)) validate-base-agent-turn) default-agent-turn))

;; In a run the default loop's own finish check asks for a test run after
;; the last change; validate is one, so a run is not asked twice.
(define validate-base-test-call?
  (or (guard (e (else #f)) validate-base-test-call?) test-call?))

(define (test-call? call)
  (or (equal? (assoc-ref call 'name) "validate") (validate-base-test-call? call)))

;; After a reminder, the turn goes on as the default chat loop does.
(define (validate-continue request)
  (let loop ((reply (ask-model)) (reading 0))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (unless (null? calls)
        (run-calls calls)
        (maybe-compact request reply)
        (loop (ask-model) (reading-rounds calls reading))))))

(define (default-agent-turn request)
  (set! validation-base (guard (e (else #f)) (git-head)))
  (let ((before (validation-state)))
    (validate-base-agent-turn request)
    (let remind ((left validate-reminders))
      (let ((now (validation-state)))
        (when (and (> left 0) (not turn-stopping) (not (equal? now before)) (not (equal? now validated-state)))
          (add-message validate-reminder)
          (validate-continue request)
          (remind (- left 1)))))))
