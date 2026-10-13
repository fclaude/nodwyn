;; What the host runs for each user message. When the turn must plan (a
;; supervised run's turns must), the plan gate runs first, so a project that
;; redefines agent-turn still plans; plan-gate does nothing once the turn has
;; a plan, so a strategy may also call it itself. A project that plans its
;; own way redefines nodwyn-turn.
(define (nodwyn-turn request)
  (set! turn-stopping #f)
  (set! stalled 0)
  (when (plan-required?) (plan-gate request))
  (agent-turn request))

;; Set when the turn must end with a report, not more work (compact.scm:
;; the context filled twice with nothing written between): from then on the
;; model is asked without tools, so its next reply is its answer and the
;; turn ends there, and the finish checks ask nothing more of it.
(define turn-stopping #f)

;; Replies cut at the output limit (window.output_reserve), usually by long
;; thinking, that ask-model asks again for, each time with a note to be
;; briefer; the core records each cut in the log.
(define output-cut-retries 2)

;; "Think less" alone was answered on xarray-6599 with "Let me be decisive:
;; the minimal fix that makes the MVCE correct is offset=None (the plan)",
;; dropping the option the model's own runs had just shown right (ADR 32,
;; amended 2026-10-10): a choice cut short goes to a run, not to the
;; shorter or the planned option.
(define output-cut-message
  "Your previous reply was cut off at the output limit before you finished. Think less and answer, or make your next tool call, more directly. If you were weighing options, do not settle them by which is shorter or which the plan named: write each in a line and run the check that tells them apart.")

;; model-turn, asked again when the reply was cut at the output limit with no
;; tool call to answer, up to output-cut-retries times: a cut-off reply would
;; otherwise end the turn, or leave the planner with a partial plan.
;; A plain (ask-model), the one that offers every tool, is also asked again
;; when the reply has no words and no tool call, up to empty-reply-retries
;; times, with a note saying so: such a reply would otherwise end the turn
;; with nothing for the person to read (ADR 32, amended 2026-10-06). The
;; planner and the asks that offer no tools have their own ways (plan-empty-retries)
;; or none, and keep them.
(define empty-reply-retries 1)

(define empty-reply-message
  "Your previous reply was empty: it said nothing and called no tool. Make your next tool call, or give your answer.")

(define (empty-reply? reply)
  (and (null? (assoc-ref reply 'tool-calls '()))
       (not (equal? (assoc-ref reply 'done "") "length"))
       (string-empty? (string-trim (format "~a" (assoc-ref reply 'content ""))))))

(define (ask-model . args)
  (let* ((plain (and (null? args) (not turn-stopping)))
         (args (if turn-stopping '("control" ((tools . #f))) args)))
    (let again ((reply (ask-model-with args)) (left empty-reply-retries))
      (if (and plain (> left 0) (empty-reply? reply))
          (begin (add-message empty-reply-message)
                 (again (ask-model-with args) (- left 1)))
          reply))))

(define (ask-model-with args)
  (let again ((reply (apply model-turn args)) (left output-cut-retries))
    (if (and (equal? (assoc-ref reply 'done "") "length")
             (null? (assoc-ref reply 'tool-calls '()))
             (> left 0))
        (begin (add-message output-cut-message)
               (again (apply model-turn args) (- left 1)))
        (begin (set! last-reasoning (assoc-ref reply 'reasoning-tokens 0))
               reply))))

;; Rounds in a row whose reply thought at length (stall-reasoning-tokens or
;; more of reasoning) and changed nothing, after which the model is told to
;; write down the design it reached and make the edit. In session
;; 20261005T151325Z-d9519026, 14 calls in a row averaged about 19,000
;; reasoning tokens each, each emitting one read and no text, the thinking
;; working out the same fix again (ADR 32, amended 2026-10-05). Its thinking
;; now goes back to it (ADR 9, amended the same day); this is for a model
;; whose does not. 0 rounds turns it off.
(define stall-rounds 3)
(define stall-reasoning-tokens 10000)
(define stall-message
  "You have thought at length for several rounds in a row without changing anything, and that thinking may not reach your next request. Write the design you have reached in a few lines, as a plan step's note (plan-step) or a plan (plan-write), then make the edit it calls for.")
(define last-reasoning 0) ; the reasoning tokens of the latest reply
(define stalled 0)        ; the rounds in a row that thought at length and changed nothing

;; After a round's calls are answered: count it if its reply thought at
;; length and none of its calls wrote anything, and say stall-message once
;; the count reaches stall-rounds.
(define (stall-check calls)
  (if (or (<= stall-rounds 0) (< last-reasoning stall-reasoning-tokens) (pair? (filter edit-call? calls)))
      (set! stalled 0)
      (begin (set! stalled (+ stalled 1))
             (when (>= stalled stall-rounds)
               (add-message stall-message)
               (set! stalled 0)))))

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
  (stall-check calls)
  (let ((after (tuned reading-reminder-after)))
    (cond ((or (<= after 0) (pair? (filter (lambda (c) (not (look-call? c))) calls))) 0)
          ((< (+ reading 1) after) (+ reading 1))
          (else (add-message (if (reviewed?)
                                 (reading-reminder-reviewed-message after)
                                 (reading-reminder-message after)))
                0))))

;; When nobody reviews the work as it happens (unattended, or a supervised
;; run), a turn ends on evidence, not on the model's word: something changed,
;; and a test ran after the last edit, and that run did not fail. A reply that
;; stops without that gets a reminder, at most finish-reminders times; a
;; failing run is told once until the next edit (finish-failing-message) and
;; the model may answer that the tests fail on the unchanged code too. A run
;; with failures counted as a run (shell.scm), so a turn could end with its
;; last test run red. The no-change reminder offers a way out with
;; evidence: after it, a turn that has run a test or check may end
;; unchanged, since the code may already do what the request asks. When a
;; person reads the answer before anything else happens ((reviewed?): a
;; chat, or a run's turn a checkpoint follows), the no-change reminder says
;; the request may ask for no change yet, a proposal to review first, and
;; after it the turn may end unchanged. Test commands that keep failing to run get
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
    "make validate" "make verify" "make ci"
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

;; Files git does not track in state that it did not show in before, and
;; files only intended for the next commit (" A", git add -N): planspec.scm
;; marks the model's new files so, and they are as new as untracked ones.
(define (git-new-files before state)
  (map git-state-path
       (filter (lambda (l) (and (or (string-prefix? "?? " l) (string-prefix? " A " l)) (not (member l before)))) state)))

;; A call's path argument as git-status names it: relative to the root.
;; Whether a repository path is a document: what a test or a check does not
;; run (planspec.scm keeps the commands that passed across its edits).
(define (doc-path? path)
  (let any ((exts '(".md" ".tex" ".rst" ".adoc")))
    (and (pair? exts) (or (string-suffix? (car exts) path) (any (cdr exts))))))

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

;; The exit status of a call that ran to an exit, #f for one that did not
;; (denied, not executed, interrupted) and for a result with no status.
(define (run-status result)
  (and (assoc-ref result 'ok #f)
       (let ((v (guard (e (else #f)) (json-parse (format "~a" (assoc-ref result 'value ""))))))
         (and (pair? v)
              (let ((status (assoc-ref v "status" #f)))
                (and (integer? status) status))))))

;; What the latest test or check run since the last edit came to, given what
;; it was before: 'none (no run yet, or it did not run to an exit and leaves
;; the answer as it was), 'failed (a non-zero exit), 'passed (exit 0),
;; 'kept (passed, and the state it passed on is kept, keep-passed), or
;; 'compared (failed, and the same run on the unchanged code failed too).
;;
;; An exit that says the tests never ran is no outcome either: a shell's 126
;; and 127 (a program not found or not executable) and pytest's 4 and 5 (a
;; path it could not find, no tests collected). On the long-context
;; benchmark (ADR 32, amended 2026-10-10) the failing-run reminder fired for
;; a flake8 that was not installed, at the end of a line whose tests had
;; passed (django-15161), and for pytest naming a file that did not exist,
;; after the work was done and reported; the second cost 35 calls
;; (scikit-learn-13124).
(define not-run-statuses '(126 127))
(define pytest-not-run-statuses '(4 5))

(define (run-outcome call result last)
  (let ((status (run-status result)))
    (cond ((not status) last)
          ((= status 0) 'passed)
          ((member status not-run-statuses) last)
          ((and (member status pytest-not-run-statuses) (string-contains? (command-line call) "pytest")) last)
          (else 'failed))))

;; A run on the copy of the last commit the working rules compare with
;; ($TMPDIR/base): it says how the unchanged code fares, not the change. A
;; failure there after the change's run failed is the comparison the
;; failing-run reminder asks for, done: the reminder fired again after a
;; model had shown the failures were there on the unchanged code too
;; (django-13158, about 12 calls).
(define (base-run? call)
  (let ((line (command-line call))
        (dir (guard (e (else #f)) (scratch-dir))))
    (or (string-contains? line "$TMPDIR/base")
        (string-contains? line "${TMPDIR}/base")
        (and dir (string-contains? line (string-append dir "/base"))))))

(define (base-outcome result last)
  (let ((status (run-status result)))
    (if (and (equal? last 'failed) status (not (= status 0)))
        'compared
        last)))

;; Whether the turn has changed the repository: the file tools wrote to it,
;; or git sees a tracked file differ from how the turn found it.
(define (turn-changed? before edited)
  (or edited
      (let ((now (and before (git-state))))
        (and now (not (equal? (git-tracked now) (git-tracked before)))))))

;; In a turn nobody reads, a test run that passed after the turn changed
;; something is a state worth going back to: the host keeps it, and puts it
;; back if a hard limit or the progress guard stops the turn later, which
;; ends this strategy and with it any chance to (ADR 18, amended
;; 2026-10-06). A turn that has changed nothing keeps nothing: the state
;; its tests passed on before any edit is no result to go back to. A keep
;; that fails is not tried again for the same run; the host remembers why.
(define (keep-passed outcome before edited)
  (if (and (equal? outcome 'passed) (unattended?) (turn-changed? before edited))
      (begin (guard (e (else #f)) (keep-passing))
             'kept)
      outcome))

;; Whether the host offers check-without (a git work tree with a scratch
;; directory): it is hidden from the model, so primitive-info says.
(define (check-without?)
  (guard (e (else #f)) (primitive-info "check-without") #t))

;; The parts of the change the passing call's check does not test: the ones
;; check-without ran it without and it still exited 0. A check-without that
;; fails says nothing, and is noted in the log.
(define (untested-parts call)
  (let* ((a (guard (e (else '())) (json-parse (format "~a" (assoc-ref call 'args "{}")))))
         (cmd (if (pair? a) (format "~a" (assoc-ref a "cmd" "")) ""))
         (args (if (pair? a)
                   (let ((l (assoc-ref a "args" '()))) (if (list? l) (map (lambda (x) (format "~a" x)) l) '()))
                   '()))
         (parts (guard (e (else (log-note (string-append "check-without failed: " (format "~a" (assoc-ref e 'message ""))))
                                '()))
                  (check-without cmd args)))
         (passed (filter (lambda (p) (equal? (assoc-ref p "status" #f) 0)) parts))
         ;; A check that passes without a part but prints something else
         ;; told it apart: the status may be one it cannot fail (; echo $?).
         (untested (filter (lambda (p) (assoc-ref p "same" #t)) passed)))
    (log-note (string-append "check-without: " (number->string (length parts)) " parts taken out, "
                             (number->string (length passed)) " the check still passed without, "
                             (number->string (length untested)) " of them printing the same"))
    untested))

;; The simple commands a shell-exec or shell-start call runs: a -c line
;; read as the shell reads it (shell-commands), so a here-document's body, a
;; quoted pattern and a redirection's target are no command.
(define (call-commands call)
  (let* ((a (guard (e (else '())) (json-parse (format "~a" (assoc-ref call 'args "{}")))))
         (cmd (if (pair? a) (format "~a" (assoc-ref a "cmd" "")) ""))
         (args (if (pair? a)
                   (let ((l (assoc-ref a "args" '()))) (if (list? l) (map (lambda (x) (format "~a" x)) l) '()))
                   '())))
    (if (string-empty? cmd) '() (guard (e (else '())) (shell-commands cmd args)))))

;; Whether two commands are the same run as a report cites it: the one, or
;; the one with more arguments after it (`go test ./...` and `go test ./...
;; -count=1`). A program's name alone is none.
(define (same-command? a b)
  (let* ((a-shorter (< (string-length a) (string-length b)))
         (shorter (if a-shorter a b))
         (longer (if a-shorter b a)))
    (and (string-contains? shorter " ")
         (or (equal? a b) (string-prefix? (string-append shorter " ") longer)))))

(define (any-command? cmd cmds)
  (let any ((cs cmds))
    (and (pair? cs) (or (same-command? cmd (car cs)) (any (cdr cs))))))

;; The repository's whole check, by the names a Makefile gives it by
;; convention, and the commands that check the code, whole or in part: once
;; the whole check has passed since the last edit, a report that cites one of
;; them stands on that run.
(define whole-check-words '("make validate" "make check" "make test" "make ci" "make verify"))

(define (whole-check? call)
  (let ((line (command-line call)))
    (let any ((ws whole-check-words))
      (and (pair? ws) (or (string-contains? line (car ws)) (any (cdr ws)))))))

(define check-command-words
  (append test-command-words
          '("make " "go build" "go vet" "gofmt" "staticcheck" "golangci-lint" "cargo build" "cargo check"
            "cargo clippy" "ruff" "flake8" "mypy" "pylint" "eslint" "tsc" "npm run" "yarn " "pnpm ")))

(define (check-command? cmd)
  (let any ((ws check-command-words))
    (and (pair? ws) (or (string-contains? cmd (car ws)) (any (cdr ws))))))

;; The text between backquotes in a reply.
(define (code-spans text)
  (let walk ((parts (string-split text "`")) (i 0) (out '()))
    (cond ((null? parts) (reverse out))
          ((odd? i) (walk (cdr parts) (+ i 1) (cons (string-trim (car parts)) out)))
          (else (walk (cdr parts) (+ i 1) out)))))

;; The commands a report cites that have not run since the last edit: spans
;; in backquotes whose first command the turn ran (same-command?), matched by
;; no command run since. In the person's sessions, "done" was claimed on the
;; strength of a run that predated the last edit (ADR 32, amended
;; 2026-10-11). A command that checks the code (check-command?) is not stale
;; once the repository's whole check has passed since the last edit
;; (checked): that run is what the report stands on. Matched by program names read off every line, the
;; reminder fired in 12 of 18 commit-bench attempts: for output the report
;; quoted (`--- PASS: TestX`, `ok in 0.4s`), whose first word a heredoc line
;; or a grep pattern had made a program, for commands never run, and for
;; make accept and go test -race ./... after a later make validate that runs
;; both (ADR 32, amended 2026-10-12).
(define (stale-citations text ran ran-all checked)
  (let walk ((spans (code-spans text)) (out '()))
    (if (null? spans)
        (reverse out)
        (let* ((span (car spans))
               (cmds (guard (e (else '())) (shell-commands "bash" (list "-c" span))))
               (cmd (and (pair? cmds) (car cmds))))
          (walk (cdr spans)
                (if (and cmd (not (member span out))
                         (any-command? cmd ran-all)
                         (not (any-command? cmd ran))
                         (not (and checked (check-command? cmd))))
                    (cons span out)
                    out))))))

;; A background run's exit status, from the first line shell-start,
;; shell-read and shell-stop give ("p1: exited 0 (the command line)"), or #f
;; while it runs. It is tool output: it decides whether the run counts and
;; goes into the result background-result makes, never into a call.
(define (proc-exit-status result)
  (and (assoc-ref result 'ok #f)
       (let* ((line (car (string-split (format "~a" (assoc-ref result 'value "")) "\n")))
              (head (string-split line ": "))
              (words (if (pair? (cdr head)) (string-split (cadr head) " ") '())))
         (and (pair? words) (equal? (car words) "exited") (pair? (cdr words))
              (let ((status (string->number (cadr words))))
                (and (integer? status) status))))))

;; The id a shell-start's result names ("p1"): tool output, only ever compared
;; with the id a later shell-read or shell-stop call names.
(define (proc-id result)
  (and (assoc-ref result 'ok #f)
       (let ((id (car (string-split (car (string-split (format "~a" (assoc-ref result 'value "")) "\n")) ":"))))
         (and (not (string-empty? id)) id))))

;; The id a shell-read or shell-stop call names, in the model's own words.
(define (proc-call-id call)
  (let ((a (guard (e (else '())) (json-parse (format "~a" (assoc-ref call 'args "{}"))))))
    (and (pair? a) (let ((id (assoc-ref a "id" #f))) (and (string? id) id)))))

;; The background runs started since the last edit are kept as two lists in
;; step: the ids their results named (ids) and the shell-start calls (calls).
;; A value made from a tainted one is tainted, so a call looked up through a
;; pair holding a tool-output id would carry the taint of the shell's output,
;; and check-without, which the guard judges, would be refused as a command
;; built from tool output: on commit-bench (nodwyn c768750, 556d6d12) that
;; ended a finished turn in an error. The ids are only compared, and a call
;; comes from the calls list alone.
(define (started-call id ids calls)
  (cond ((or (not id) (null? ids) (null? calls)) #f)
        ((equal? (car ids) id) (car calls))
        (else (started-call id (cdr ids) (cdr calls)))))

(define (started-calls-without id ids calls)
  (cond ((or (null? ids) (null? calls)) '())
        ((equal? (car ids) id) (started-calls-without id (cdr ids) (cdr calls)))
        (else (cons (car calls) (started-calls-without id (cdr ids) (cdr calls))))))

(define (started-ids-without id ids)
  (filter (lambda (i) (not (equal? i id))) ids))

;; A background run that has exited, as the shell-exec call it amounts to:
;; the command shell-start ran (start), under the id of the call that saw it
;; exit. Read with waits of 60 seconds, the repository's whole check often
;; ran this way, and the loop, which counted shell-exec runs only, then asked
;; for the tests to be run after the last edit when they had been (5 of the 6
;; such reminders on commit-bench; ADR 32, amended 2026-10-12).
(define (background-call call start)
  (list (cons 'name "shell-exec") (cons 'id (assoc-ref call 'id ""))
        (cons 'args (assoc-ref start 'args "{}"))))

;; Its result: the status it exited with, as shell-exec reports one.
(define (background-result status)
  (list (cons 'ok #t) (cons 'value (string-append "{\"status\": " (number->string status) "}"))))

;; Files new in the repository that the turn's plan names: the test file a
;; Test: step adds, the release note a Docs: step writes. The untracked-files
;; reminder is for what a run left behind, and on the long-context benchmark
;; it fired on the files the plan had asked for in 13 of 107 attempts, each
;; answered by keeping them, after a call or four (ADR 32, amended
;; 2026-10-10). A file is named when the plan gives its path or its name.
(define (plan-names? path)
  (let* ((text (guard (e (else "")) (string-join (plan-steps) "\n")))
         (p (if (string-suffix? "/" path) (substring path 0 (- (string-length path) 1)) path))
         (name (car (reverse (string-split p "/")))))
    (and (not (string-empty? name))
         (or (string-contains? text p) (string-contains? text name)))))

;; Whether the plan's tests ran by the harness's hand in the turn: a plan as
;; data (planspec.scm) redefines it.
(define (plan-language-auto-tested?) #f)

(define (evidence-loop request)
  ;; passing: the latest check or test call that exited 0 since the last
  ;; edit, for check-without. ran: the command lines run since the last edit,
  ;; and every program the turn ran, for the report's citations. ablated and
  ;; cited: whether those two checks were made, once a turn each. named: the
  ;; new files the untracked-files reminder named, which check-without's
  ;; answer leaves out rather than name them twice.
  ;; ran-all: the commands run in the turn; base-ran: those run on the copy
  ;; of the last commit, which no edit makes stale; started-ids and
  ;; started-calls: the background runs started since the last edit
  ;; (started-call).
  ;; passed: the command lines that exited 0 since the last edit; planned:
  ;; whether the plan's evidence was checked (planspec.scm), once a turn.
  (let ((before (git-state)) (passing #f) (ran '()) (ran-all '()) (base-ran '()) (started-ids '()) (started-calls '())
        (passed '()) (planned #f) (ablated #f) (cited #f) (named '()))
    ;; written: the repository paths the file tools changed, for git to
    ;; place.
    ;; excused: a reviewed turn was told it may end unchanged.
    ;; reading: the rounds in a row that only looked (reading-rounds).
    ;; outcome: what the latest test or check run since the last edit came to
    ;; (run-outcome).
    ;; told: the failing-run reminder was said since the last edit, so a run
    ;; that fails the same way again is not told again until something
    ;; changes.
    ;; unchanged-told: the no-change reminder was said; a turn that has run a
    ;; check since may then end unchanged, the way out the reminder offers.
    (let loop ((reply (ask-model)) (edited #f) (tested #f) (failed-runs 0) (offered #f) (reminders 0)
               (scripts '()) (written '()) (noted #f) (excused #f) (reading 0) (outcome 'none)
               (told #f) (unchanged-told #f))
      (let ((calls (assoc-ref reply 'tool-calls '())))
        (if (pair? calls)
            (let walk ((cs calls) (rs (run-calls calls)) (edited edited) (tested tested) (failed failed-runs)
                       (scripts scripts) (written written) (outcome outcome) (told told))
              (cond ((pair? cs)
                     (let* ((c0 (car cs)) (r0 (car rs))
                            (name0 (assoc-ref c0 'name))
                            (status (and (member name0 '("shell-start" "shell-read" "shell-stop"))
                                         (proc-exit-status r0)))
                            (start (and status (if (equal? name0 "shell-start")
                                                   c0
                                                   (started-call (proc-call-id c0) started-ids started-calls))))
                            (c (if start (background-call c0 start) c0))
                            (r (if start (background-result status) r0)))
                       (when (member name0 '("shell-exec" "shell-start"))
                         (let ((cmds (call-commands c0)))
                           (set! ran (append cmds ran))
                           (set! ran-all (append cmds ran-all))
                           (when (base-run? c0)
                             (set! base-ran (append cmds base-ran)))))
                       ;; A run is counted once, when it is seen to exit.
                       (cond ((and (equal? name0 "shell-start") (not status) (proc-id r0))
                              (set! started-ids (cons (proc-id r0) started-ids))
                              (set! started-calls (cons c0 started-calls)))
                             ((and status (not (equal? name0 "shell-start")))
                              (let ((id (proc-call-id c0)))
                                (set! started-calls (started-calls-without id started-ids started-calls))
                                (set! started-ids (started-ids-without id started-ids)))))
                       (when (and (not (base-run? c)) (or (check-run? c scripts) (test-call? c))
                                  (equal? (run-status r) 0))
                         (set! passing c))
                       (when (and (equal? (assoc-ref c 'name) "shell-exec") (not (base-run? c)) (equal? (run-status r) 0))
                         (set! passed (cons (command-line c) passed)))
                       ;; A document's edit leaves the plan's evidence standing
                       ;; (passed): no test or check reads a .md or a .tex.
                       (when (and (edit-call? c) (assoc-ref r 'ok #f) (not (scratch-name c)) (not (doc-path? (repo-path c))))
                         (set! passed '()))
                       (when (and (edit-call? c) (assoc-ref r 'ok #f) (not (scratch-name c)) (not (doc-path? (repo-path c))))
                         (set! passing #f)
                         (set! ran '())
                         (set! started-ids '())
                         (set! started-calls '()))
                       (cond ((and (edit-call? c) (assoc-ref r 'ok #f))
                              (let ((name (scratch-name c)))
                                (cond (name
                                       (walk (cdr cs) (cdr rs) edited tested failed
                                             (if (member name scripts) scripts (cons name scripts)) written outcome told))
                                      ;; A document's edit is a change, and no test
                                      ;; or check reads it: the runs before it stand
                                      ;; (commit-bench, 2026-10-13: a turn that edited
                                      ;; its ADR last was told it had not tested).
                                      ((doc-path? (repo-path c))
                                       (walk (cdr cs) (cdr rs) #t tested failed scripts (cons (repo-path c) written) outcome told))
                                      (else
                                       (walk (cdr cs) (cdr rs) #t #f failed scripts (cons (repo-path c) written) 'none #f)))))
                             ;; A run on the unchanged copy tests nothing of
                             ;; the change; it can only show a failure was
                             ;; there before it.
                             ((and (base-run? c) (or (test-call? c) (check-run? c scripts)))
                              (walk (cdr cs) (cdr rs) edited tested failed scripts written (base-outcome r outcome) told))
                             ;; A check fails until the work is done: only a
                             ;; run that went through says the setup works.
                             ((check-run? c scripts)
                              (walk (cdr cs) (cdr rs) edited #t (if (ran-ok? r) 0 failed) scripts written (run-outcome c r outcome) told))
                             ((test-call? c)
                              (walk (cdr cs) (cdr rs) edited #t (if (ran-ok? r) 0 (+ failed 1)) scripts written (run-outcome c r outcome) told))
                             (else (walk (cdr cs) (cdr rs) edited tested failed scripts written outcome told)))))
                    (else
                     (maybe-compact request reply)
                     (let ((stuck (and (not offered) (>= failed unrunnable-after)))
                           (reading (reading-rounds calls reading))
                           (outcome (keep-passed outcome before edited)))
                       (when stuck (add-message (finish-unrunnable-message failed)))
                       (loop (ask-model) edited tested failed (or offered stuck) reminders scripts written noted excused
                             reading outcome told unchanged-told)))))
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
                   (gap (cond (turn-stopping #f)
                              ((and (not changed) (not excused) (not (and unchanged-told tested)))
                               (if reviewed finish-no-change-reviewed-message finish-no-change-message))
                              ((and changed (not tested) (not (plan-language-auto-tested?)) (not offered)) finish-untested-message)
                              ((and changed (equal? outcome 'failed) (not offered) (not told)) finish-failing-message)
                              (else #f)))
                   (new (if (and now changed (not noted))
                            (filter (lambda (f) (not (plan-names? f))) (git-new-files before now))
                            '())))
              (cond ((and gap (< reminders finish-reminders))
                     (add-message gap)
                     ;; A run the model was told failed is not told again
                     ;; until an edit: it may answer that the tests fail on the
                     ;; unchanged code too, and end.
                     (loop (ask-model) edited tested failed-runs offered (+ reminders 1) scripts written noted
                           (or excused (and (not changed) reviewed)) 0 outcome
                           (or told (equal? gap finish-failing-message))
                           (or unchanged-told (not changed))))
                    ((and (pair? new) (not turn-stopping))
                     (set! named new)
                     (add-message (finish-new-files-message new))
                     (loop (ask-model) edited tested failed-runs offered reminders scripts written #t excused 0 outcome
                           told unchanged-told))
                    ;; The plan's steps set against what happened, when the
                    ;; plan is data (planspec.scm): what lacks its evidence
                    ;; is said once; what has it is marked.
                    ((and changed (not turn-stopping) (not planned) plan-language (pair? plan-spec))
                     (set! planned #t)
                     (let* ((check (plan-language-check passed)) (unmet (car check)))
                       (plan-language-mark (cadr check))
                       (if (pair? unmet)
                           (begin (add-message (plan-language-unmet-message unmet))
                                  (loop (ask-model) edited tested failed-runs offered reminders scripts written noted excused 0
                                        outcome told unchanged-told))
                           (loop reply edited tested failed-runs offered reminders scripts written noted excused 0
                                 outcome told unchanged-told))))
                    ;; The change's parts the passing check does not test
                    ;; (check-without), asked once a turn.
                    ((and changed (not turn-stopping) (not ablated) passing (memq outcome '(passed kept)) (check-without?))
                     (set! ablated #t)
                     (let ((parts (filter (lambda (p) (not (member (assoc-ref p "path" "") named))) (untested-parts passing))))
                       (if (pair? parts)
                           (begin (add-message (finish-untested-parts-message (command-line passing) parts))
                                  (loop (ask-model) edited tested failed-runs offered reminders scripts written noted excused 0
                                        outcome told unchanged-told))
                           (loop reply edited tested failed-runs offered reminders scripts written noted excused 0
                                 outcome told unchanged-told))))
                    ;; A command the report cites that has not run since the
                    ;; last edit, asked once a turn.
                    ((and changed (not turn-stopping) (not cited)
                          (pair? (stale-citations (format "~a" (assoc-ref reply 'content "")) (append ran base-ran) ran-all (and passing (whole-check? passing)))))
                     (set! cited #t)
                     (add-message (finish-stale-citations-message
                                   (stale-citations (format "~a" (assoc-ref reply 'content "")) (append ran base-ran) ran-all (and passing (whole-check? passing)))))
                     (loop (ask-model) edited tested failed-runs offered reminders scripts written noted excused 0
                           outcome told unchanged-told)))))))))
