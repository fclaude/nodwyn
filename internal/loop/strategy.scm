;; What the host runs for each user message. When the turn must plan (a
;; supervised run's turns must), the plan gate runs first, so a project that
;; redefines agent-turn still plans; plan-gate does nothing once the turn has
;; a plan, so a strategy may also call it itself. A project that plans its
;; own way redefines nodwyn-turn.
(define (nodwyn-turn request)
  (when (plan-required?) (plan-gate request))
  (agent-turn request))

;; Replies cut at the output limit (window.output_reserve), usually by long
;; thinking, that ask-model asks again for, each time with a note to be
;; briefer; the core records each cut in the log.
(define output-cut-retries 2)

(define output-cut-message
  "Your previous reply was cut off at the output limit before you finished. Think less and answer, or make your next tool call, more directly.")

;; model-turn, asked again when the reply was cut at the output limit with no
;; tool call to answer, up to output-cut-retries times: a cut-off reply would
;; otherwise end the turn, or leave the planner with a partial plan.
(define (ask-model . args)
  (let again ((reply (apply model-turn args)) (left output-cut-retries))
    (if (and (equal? (assoc-ref reply 'done "") "length")
             (null? (assoc-ref reply 'tool-calls '()))
             (> left 0))
        (begin (add-message output-cut-message)
               (again (apply model-turn args) (- left 1)))
        reply)))

;; The default agent loop: first the plan gate (plan.scm), then ask the
;; model; while it asks for tools, run them (agent tools through their
;; sub-agents, agents.scm) and ask again; a reply without tool calls ends the
;; turn. Between rounds the context is compacted once it passes
;; compact-threshold (compact.scm; just under the window's high water by
;; default). A request starting with /compact compacts the conversation now
;; instead (compact.scm), and one starting with /optimize runs the optimizer
;; (optimize.scm). A project may redefine agent-turn in .nodwyn/init.scm
;; (docs/scheme.md §9).
(define (agent-turn request)
  (cond ((compact-request? request) (compact-turn request))
        ((optimize-request? request) (optimize-turn request))
        (else (default-agent-turn request))))

(define (default-agent-turn request)
  (plan-gate request)
  (if (or (unattended?) (plan-required?))
      (evidence-loop request)
      (let loop ((reply (ask-model)))
        (let ((calls (assoc-ref reply 'tool-calls '())))
          (unless (null? calls)
            (run-calls calls)
            (maybe-compact request reply)
            (loop (ask-model)))))))

;; When nobody reviews the work as it happens (unattended, or a supervised
;; run), a turn ends on evidence, not on the model's word: something changed,
;; and a test ran after the last edit. A reply that stops without that gets a
;; reminder, at most finish-reminders times. Test commands that keep failing
;; to run get finish-unrunnable-message once, so a broken environment ends
;; in an honest report rather than a spent budget.
(define finish-reminders 2)
(define unrunnable-after 6) ; failed test commands in a row before the way out is offered

;; Words that mark a command as running tests, across ecosystems; a project
;; may redefine the list.
(define test-command-words
  '("pytest" "unittest" "runtests" "tox" "nox" "bin/test" "go test" "cargo test"
    "npm test" "npm run test" "yarn test" "pnpm test" "jest" "vitest" "mocha"
    "mvn test" "gradle test" "gradlew test" "ctest" "make test" "make check"
    "rspec" "rake test" "phpunit" "dotnet test" "mix test" "swift test"))

;; A shell-exec call's command line, cmd and args joined.
(define (command-line call)
  (let ((args (guard (e (else '())) (json-parse (assoc-ref call 'args "{}")))))
    (if (pair? args)
        (string-join (cons (format "~a" (assoc-ref args "cmd" ""))
                           (map (lambda (a) (format "~a" a)) (let ((l (assoc-ref args "args" '()))) (if (list? l) l '()))))
                     " ")
        "")))

(define (test-call? call)
  (and (equal? (assoc-ref call 'name) "shell-exec")
       (let ((line (command-line call)))
         (let any ((ws test-command-words))
           (and (pair? ws) (or (string-contains? line (car ws)) (any (cdr ws))))))))

(define (edit-call? call) (member (assoc-ref call 'name) '("fs-write" "fs-edit")))

;; Whether a test command ran successfully: exit status 0.
(define (ran-ok? result)
  (and (assoc-ref result 'ok #f)
       (let ((v (guard (e (else #f)) (json-parse (format "~a" (assoc-ref result 'value ""))))))
         (and (pair? v) (equal? (assoc-ref v "status" 1) 0)))))

(define (evidence-loop request)
  (let loop ((reply (ask-model)) (edited #f) (tested #f) (failed-runs 0) (offered #f) (reminders 0))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (if (pair? calls)
          (let walk ((cs calls) (rs (run-calls calls)) (edited edited) (tested tested) (failed failed-runs))
            (cond ((pair? cs)
                   (let ((c (car cs)) (r (car rs)))
                     (cond ((and (edit-call? c) (assoc-ref r 'ok #f))
                            (walk (cdr cs) (cdr rs) #t #f failed))
                           ((test-call? c)
                            (walk (cdr cs) (cdr rs) edited #t (if (ran-ok? r) 0 (+ failed 1))))
                           (else (walk (cdr cs) (cdr rs) edited tested failed)))))
                  (else
                   (maybe-compact request reply)
                   (let ((stuck (and (not offered) (>= failed unrunnable-after))))
                     (when stuck (add-message (finish-unrunnable-message failed)))
                     (loop (ask-model) edited tested failed (or offered stuck) reminders)))))
          (let ((gap (cond ((not edited) finish-no-change-message)
                           ((and (not tested) (not offered)) finish-untested-message)
                           (else #f))))
            (when (and gap (< reminders finish-reminders))
              (add-message gap)
              (loop (ask-model) edited tested failed-runs offered (+ reminders 1))))))))
