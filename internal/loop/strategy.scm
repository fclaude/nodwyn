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

;; A knob that depends on the model is a number, which holds for every
;; model, or a table: rows of (family . value) and a (default . value) row,
;;   '(("glm" . 20) (default . 12))
;; A family is the front of the model's id as its provider spells it, without
;; any "vendor/" ("qwen" fits qwen3.8-27b and qwen/qwen3.8-27b alike), and the
;; first row that fits wins, so a narrower family goes before a broader one. A
;; model no row fits gets the default row. The model is the control model as
;; it is now, so switching it in a chat changes the value from the next use.
;; The tables are plan-explore-rounds (plan.scm) and reading-reminder-after.
(define (model-family-id)
  (let ((parts (string-split (guard (e (else "")) (format "~a" (assoc-ref (model-info) 'model ""))) "/")))
    (if (null? parts) "" (car (reverse parts)))))

(define (tuned knob)
  (if (not (list? knob))
      knob
      ;; The model is asked about only when a family row could use it.
      (let ((id (and (pair? (filter (lambda (row) (string? (car row))) knob)) (model-family-id))))
        (let find ((rows knob) (fallback #f))
          (cond ((null? rows) fallback)
                ((equal? (car (car rows)) 'default) (find (cdr rows) (cdr (car rows))))
                ((string-prefix? (car (car rows)) id) (cdr (car rows)))
                (else (find (cdr rows) fallback)))))))

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
      (let loop ((reply (ask-model)) (reading 0))
        (let ((calls (assoc-ref reply 'tool-calls '())))
          (unless (null? calls)
            (run-calls calls)
            (maybe-compact request reply)
            (let ((reading (reading-rounds calls reading)))
              (loop (ask-model) reading)))))))

;; Tools that only look at the repository: a round whose calls are all
;; among them neither changes nor runs anything. A project that adds tools
;; of its own that only look may add them to the list.
(define look-tools
  '("fs-read" "fs-list" "fs-find" "index-grep" "read-definition" "git-status" "git-diff"))

(define (look-call? call) (member (assoc-ref call 'name) look-tools))

;; Rounds in a row that only look, after which the model is told to write
;; down what it found and answer or plan (reading-reminder-message). A chat
;; turn (ADR 32, amended 2026-10-02) had what it needed by its sixth round
;; and read on for 34 more, saying nothing: its thinking was not sent back
;; then, and is a summary when it is, so every request began again from
;; the tool results and read more to confirm the same conclusion. 0 turns the reminder off.
;;
;; A table (tuned): no family has a row. In the local session logs (73
;; sessions, 2026-10-05) the reminder at 12 ended the reading in the next
;; round for 8 of 11 muse-spark turns; glm, qwen and deepseek often read on
;; after it (16 of 27, 5 of 13 and 0 of 2 stopped at once), which another
;; count would not change; and gpt-6.1-sol and gpt-6-luna never read 12
;; rounds in a row (2 to 3 calls a round; longest runs 7 and 5), claude-sonnet
;; once. Add a row when a family's logs show 12 is wrong for it.
(define reading-reminder-after '((default . 12)))

;; The rounds in a row that only looked, counting this round's calls: when
;; they reach reading-reminder-after the reminder is added, after the
;; calls are answered, and the count starts again.
(define (reading-rounds calls reading)
  (let ((after (tuned reading-reminder-after)))
    (cond ((or (<= after 0) (pair? (filter (lambda (c) (not (look-call? c))) calls))) 0)
          ((< (+ reading 1) after) (+ reading 1))
          (else (add-message (if (reviewed?)
                                 (reading-reminder-reviewed-message after)
                                 (reading-reminder-message after)))
                0))))

;; When nobody reviews the work as it happens (unattended, or a supervised
;; run), a turn ends on evidence, not on the model's word: something changed,
;; and a test ran after the last edit. A reply that stops without that gets a
;; reminder, at most finish-reminders times. When a person reads the answer
;; before anything else happens ((reviewed?): a chat, or a run's turn a
;; checkpoint follows), the no-change reminder says the request may ask for
;; no change yet, a proposal to review first, and after it the turn may end
;; unchanged. Test commands that keep failing to run get
;; finish-unrunnable-message once, so a broken environment ends in an honest
;; report rather than a spent budget.
;; The scratch directory is not the repository: writing the check script
;; there is not a change, and running it is a test run, as the check loop
;; asks (prompts.scm). In a git work tree git says what changed, edits made
;; through the shell included, and files new since the turn began that git
;; does not track get finish-new-files-message once: a stray script or
;; backup would end up in the change.
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

;; How a model's call may spell a path in the scratch directory: $TMPDIR,
;; as the prompts name it, or the directory itself.
(define (scratch-prefixes)
  (let ((dir (guard (e (else #f)) (scratch-dir))))
    (append '("$TMPDIR/" "${TMPDIR}/") (if dir (list (string-append dir "/")) '()))))

;; The name inside the scratch directory of a call's path argument, or #f
;; when the path is not in it.
(define (scratch-name call)
  (let* ((a (guard (e (else '())) (json-parse (format "~a" (assoc-ref call 'args "{}")))))
         (path (if (pair? a) (format "~a" (assoc-ref a "path" "")) "")))
    (let try ((ps (scratch-prefixes)))
      (cond ((null? ps) #f)
            ((and (string-prefix? (car ps) path) (> (string-length path) (string-length (car ps))))
             (substring path (string-length (car ps)) (string-length path)))
            (else (try (cdr ps)))))))

;; Programs that only look at, move or remove a file: naming a script is
;; not running it.
(define check-run-skip '("cat" "head" "tail" "less" "more" "ls" "wc" "file" "stat" "chmod" "cp" "mv" "rm"))

;; A shell-exec call that runs one of the scripts the model wrote in the
;; scratch directory (scripts: their names there), such as its check.
(define (check-run? call scripts)
  (and (pair? scripts)
       (equal? (assoc-ref call 'name) "shell-exec")
       (let* ((a (guard (e (else '())) (json-parse (format "~a" (assoc-ref call 'args "{}")))))
              (cmd (if (pair? a) (format "~a" (assoc-ref a "cmd" "")) ""))
              (line (command-line call)))
         (and (not (member (car (reverse (string-split cmd "/"))) check-run-skip))
              (let any ((ss scripts))
                (and (pair? ss)
                     (or (let spelled ((ps (scratch-prefixes)))
                           (and (pair? ps)
                                (or (string-contains? line (string-append (car ps) (car ss)))
                                    (spelled (cdr ps)))))
                         (any (cdr ss)))))))))

;; The repository as git sees it: the lines of (git-status) after the branch
;; line, or #f where there is no git-status (not a git work tree) or git
;; fails.
(define (git-state)
  (and (guard (e (else #f)) (primitive-info "git-status") #t)
       (guard (e (else #f))
         (filter (lambda (l) (not (or (equal? (string-trim l) "") (string-prefix? "## " l))))
                 (string-split (format "~a" (git-status)) "\n")))))

;; The path in a line of (git-state).
(define (git-state-path line) (if (> (string-length line) 3) (substring line 3 (string-length line)) ""))

;; The lines of state for files git tracks.
(define (git-tracked state) (filter (lambda (l) (not (string-prefix? "?? " l))) state))

;; Files git does not track in state that it did not show in before.
(define (git-new-files before state)
  (map git-state-path
       (filter (lambda (l) (and (string-prefix? "?? " l) (not (member l before)))) state)))

;; A call's path argument as git-status names it: relative to the root.
(define (repo-path call)
  (let* ((a (guard (e (else '())) (json-parse (format "~a" (assoc-ref call 'args "{}")))))
         (p (if (pair? a) (format "~a" (assoc-ref a "path" "")) ""))
         (root (guard (e (else #f)) (repository))))
    (cond ((string-prefix? "./" p) (substring p 2 (string-length p)))
          ((and root (string-prefix? (string-append root "/") p))
           (substring p (+ (string-length root) 1) (string-length p)))
          (else p))))

;; Whether git shows one of paths in lines, itself or in an untracked
;; directory.
(define (git-shows? paths lines)
  (let any ((ls lines))
    (and (pair? ls)
         (let ((shown (git-state-path (car ls))))
           (or (let match ((ps paths))
                 (and (pair? ps)
                      (or (equal? shown (car ps))
                          (and (string-suffix? "/" shown) (string-prefix? shown (car ps)))
                          (match (cdr ps)))))
               (any (cdr ls)))))))

(define (evidence-loop request)
  (let ((before (git-state)))
    ;; written: the repository paths the file tools changed, for git to
    ;; place.
    ;; excused: a reviewed turn was told it may end unchanged.
    ;; reading: the rounds in a row that only looked (reading-rounds).
    (let loop ((reply (ask-model)) (edited #f) (tested #f) (failed-runs 0) (offered #f) (reminders 0)
               (scripts '()) (written '()) (noted #f) (excused #f) (reading 0))
      (let ((calls (assoc-ref reply 'tool-calls '())))
        (if (pair? calls)
            (let walk ((cs calls) (rs (run-calls calls)) (edited edited) (tested tested) (failed failed-runs)
                       (scripts scripts) (written written))
              (cond ((pair? cs)
                     (let ((c (car cs)) (r (car rs)))
                       (cond ((and (edit-call? c) (assoc-ref r 'ok #f))
                              (let ((name (scratch-name c)))
                                (if name
                                    (walk (cdr cs) (cdr rs) edited tested failed
                                          (if (member name scripts) scripts (cons name scripts)) written)
                                    (walk (cdr cs) (cdr rs) #t #f failed scripts (cons (repo-path c) written)))))
                             ;; A check fails until the work is done: only a
                             ;; run that went through says the setup works.
                             ((check-run? c scripts)
                              (walk (cdr cs) (cdr rs) edited #t (if (ran-ok? r) 0 failed) scripts written))
                             ((test-call? c)
                              (walk (cdr cs) (cdr rs) edited #t (if (ran-ok? r) 0 (+ failed 1)) scripts written))
                             (else (walk (cdr cs) (cdr rs) edited tested failed scripts written)))))
                    (else
                     (maybe-compact request reply)
                     (let ((stuck (and (not offered) (>= failed unrunnable-after)))
                           (reading (reading-rounds calls reading)))
                       (when stuck (add-message (finish-unrunnable-message failed)))
                       (loop (ask-model) edited tested failed (or offered stuck) reminders scripts written noted excused
                             reading)))))
            (let* ((now (and before (git-state)))
                   (new-lines (if now (filter (lambda (l) (not (member l before))) now) '()))
                   ;; With git: a tracked file changed, the file tools wrote
                   ;; a file git did not show before, or one it already
                   ;; showed, whose further edits the status cannot show.
                   ;; Files a run left behind are no change.
                   (changed (if now
                                (or (not (equal? (git-tracked now) (git-tracked before)))
                                    (git-shows? written new-lines)
                                    (git-shows? written before))
                                edited))
                   (reviewed (reviewed?))
                   (gap (cond ((and (not changed) (not excused))
                               (if reviewed finish-no-change-reviewed-message finish-no-change-message))
                              ((and changed (not tested) (not offered)) finish-untested-message)
                              (else #f)))
                   (new (if (and now changed (not noted)) (git-new-files before now) '())))
              (cond ((and gap (< reminders finish-reminders))
                     (add-message gap)
                     (loop (ask-model) edited tested failed-runs offered (+ reminders 1) scripts written noted
                           (or excused (and (not changed) reviewed)) 0))
                    ((pair? new)
                     (add-message (finish-new-files-message new))
                     (loop (ask-model) edited tested failed-runs offered reminders scripts written #t excused 0)))))))))
