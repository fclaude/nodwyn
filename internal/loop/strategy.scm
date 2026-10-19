;; What the host runs for each user message. When the turn must plan (a
;; supervised run's turns must), the plan gate runs first, so a project that
;; redefines agent-turn still plans; plan-gate does nothing once the turn has
;; a plan, so a strategy may also call it itself. A project that plans its
;; own way redefines nodwyn-turn.
(define (nodwyn-turn request)
  (when (plan-required?) (plan-gate request))
  (agent-turn request))

;; What the host calls with the request before nodwyn-turn, every turn, so
;; the state a turn keeps for itself starts afresh even when a project
;; redefines nodwyn-turn: the habits' own files wrap it to reset theirs. A
;; project that redefines nodwyn-turn but not turn-start keeps every reset;
;; one that redefines turn-start wraps the version it replaces.
(define (turn-start request)
  (set! turn-stopping #f)
  (set! turn-stop-tools '())
  (set! turn-stop-rounds 0)
  (set! stalled 0))

;; Set when the turn must end with a report, not more work (compact.scm:
;; the context filled twice with nothing written between): from then on the
;; model is offered only turn-stop-tools, and no tool at all once their
;; rounds are spent, so a reply without calls is its answer and the turn
;; ends there, and the finish checks ask nothing more of it.
(define turn-stopping #f)

;; While the turn stops, the tools the model is still offered and the
;; requests it may still use them in (ask-stopping); compaction's write round
;; sets them (compact.scm). A call to any other tool is not run. Once the
;; rounds are spent, turn-stop-report is said and the model is asked with no
;; tools.
(define turn-stop-tools '())
(define turn-stop-rounds 0)
(define turn-stop-report
  "The rounds you had for that are spent, and tools are off now. In a few lines say what you changed, what is left and what stands in the way; the turn ends with this answer.")

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
  (if turn-stopping
      (ask-stopping)
      (let again ((reply (ask-model-with args)) (left empty-reply-retries))
        (if (and (null? args) (> left 0) (empty-reply? reply))
            (begin (add-message empty-reply-message)
                   (again (ask-model-with args) (- left 1)))
            reply))))

;; ask-model while the turn stops (turn-stopping): the control model,
;; offered turn-stop-tools while turn-stop-rounds last and nothing after
;; them. A model asked with few tools or none may call others it has seen
;; (compact-note skips such calls for the same reason); they are skipped
;; here, and a reply whose every call was skipped spends its round and is
;; asked again, rather than end the turn with nothing said.
(define (ask-stopping)
  (when (and (pair? turn-stop-tools) (<= turn-stop-rounds 0))
    (set! turn-stop-tools '())
    (add-message turn-stop-report))
  (let* ((offer turn-stop-tools)
         (reply (ask-model-with (list "control" (list (cons 'tools (if (pair? offer) offer #f))))))
         (calls (assoc-ref reply 'tool-calls '()))
         (offered? (lambda (c) (member (assoc-ref c 'name) offer)))
         (off (filter (lambda (c) (not (offered? c))) calls)))
    (when (pair? offer)
      (set! turn-stop-rounds (- turn-stop-rounds 1)))
    (if (null? off)
        reply
        (let ((kept (filter offered? calls)))
          (skip-tools off (if (pair? offer)
                              (string-append "the turn is ending, and only " (string-join offer ", ") " may be used now")
                              "tools are off now: write your answer as text"))
          (if (and (null? kept) (pair? offer))
              (ask-stopping)
              (cons (cons 'tool-calls kept) reply))))))

(define (lean-args args)
  (if (and lean-prompt (null? args))
      (list "control" (list (cons 'tools lean-tools)))
      args))

(define (ask-model-with args)
  (let again ((reply (apply model-turn (lean-args args))) (left output-cut-retries))
    (if (and (equal? (assoc-ref reply 'done "") "length")
             (null? (assoc-ref reply 'tool-calls '()))
             (> left 0))
        (begin (add-message output-cut-message)
               (again (apply model-turn (lean-args args)) (- left 1)))
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

;; Tools that only look, at the repository or at the conversation: a round
;; whose calls are all among them neither changes nor runs anything. A
;; project that adds tools of its own that only look may add them to the
;; list.
(define look-tools
  '("fs-read" "fs-list" "fs-find" "index-grep" "read-definition" "git-status" "git-diff"
    "index-search" "recall"))

;; A shell-exec call that only looks too: it reads, searches or lists, and
;; writes, runs and changes nothing. On the benchmark's matplotlib-25960 the
;; shell2 arm (shell-exec and fs-edit only) made 21 read-only calls and ended
;; its turn with no edit: the nudge above counted rounds of the dedicated
;; read tools only, so grep, sed -n and cat through the shell never added up
;; (ADR 32, amended 2026-10-17).
(define shell-look-words '("grep " "rg " "find " "ls " "ls\n" "cat " "head " "tail " "sed -n" "wc " "nl " "which " "git log" "git show" "git diff" "git status" "git blame" "git grep" "tree "))
(define shell-act-words '(">" "sed -i" "tee " "python" "pytest" "make " "pip " "conda " "npm " "git checkout" "git stash" "git apply" "git reset" "git commit" "rm " "mv " "cp " "mkdir " "chmod " "patch " "perl "))

(define (shell-look? call)
  (and (equal? (assoc-ref call 'name) "shell-exec")
       (let ((line (command-line call)))
         (and (not (string-empty? line))
              (let any ((ws shell-look-words)) (and (pair? ws) (or (string-contains? line (car ws)) (any (cdr ws)))))
              (not (let any ((ws shell-act-words)) (and (pair? ws) (or (string-contains? line (car ws)) (any (cdr ws))))))))))

;; lean-shell-look is on in the lean profile and off in the full one, which
;; counted the read tools alone (full.scm; docs/scheme.md §9.8).
(define lean-shell-look #t)

(define (look-call? call) (or (member (assoc-ref call 'name) look-tools) (and lean-shell-look (shell-look? call))))

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
;; The lean profile has no reading reminder (2026-10-17): of 47 firings read,
;; about 8 weakly helped, 20 changed nothing, 15 were false alarms and up to 4
;; may have hurt, and it told the model to use plan-write, which the lean
;; profile does not offer (ADR 32, amended 2026-10-17).
(define lean-no-read-reminder #t)

;; The rounds in a row that only looked, counting this round's calls: when
;; they reach reading-reminder-after the reminder is added, after the
;; calls are answered, and the count starts again.
(define (reading-rounds calls reading)
  (stall-check calls)
  (let ((after (if lean-no-read-reminder 0 (tuned reading-reminder-after))))
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
;; The lean ladder, rung 2 (2026-10-16): lean-quiet drops what the harness says
;; to the model while it works and as it ends: the finish review, its
;; reminders, the question on an edited test, the hint on failures the
;; unchanged code has. It keeps what keeps a run safe (the guard, the budget,
;; the progress guard, the context window). Of dsl23's 4,385 requests in 121
;; tasks, 707 came after the finish review began, and 17% of its cost.
(define lean-quiet #t)
(define finish-reminders (if lean-quiet 0 2))
;; Even then a turn that changed nothing is told so once: shell2 on
;; matplotlib-25960 ended its turn with no edit and no patch.
(define lean-empty-reminders (if lean-quiet 1 0))
(define unrunnable-after 6) ; failed test commands in a row before the way out is offered

;; The finish review (finish-review-points) is made once a turn, and at most
;; finish-review-limit times in a run nobody reads as it goes: each is the
;; passing check run again with each part of the change taken out, and what
;; it asks for. Past the limit a turn that changed something ends on its
;; report. A turn that edited nothing has no review, so the second needs
;; new edits as the first does. A run whose answers a person reads
;; ((reviewed?)) has none of the limit; the count is the session's, and a
;; resumed run starts it again (ADR 32, amended 2026-10-16: commit-bench
;; spent 9-38% of its runs' tokens in the closing rounds).
(define finish-review-limit (if lean-quiet 0 2))
(define finish-reviews 0)

(define (finish-review-due?)
  (or (reviewed?) (< finish-reviews finish-review-limit)))

(define (finish-review-counted!)
  (unless (reviewed?) (set! finish-reviews (+ finish-reviews 1))))

;; Words that mark a command as running tests, across ecosystems; a project
;; may redefine the list.
(define test-command-words
  '("pytest" "unittest" "runtests" "tox" "nox" "bin/test" "go test" "cargo test"
    "npm test" "npm run test" "yarn test" "pnpm test" "jest" "vitest" "mocha"
    "mvn test" "gradle test" "gradlew test" "ctest" "make test" "make check"
    "make validate" "go run ./scripts/validate" "make verify" "make ci"
    "rspec" "rake test" "phpunit" "dotnet test" "mix test" "swift test"))

;; The tools a repository registers to run a command for the model, as the
;; command each runs: the tool, the program, the words before the tool's
;; argument, and that argument's name (#f for none). A trusted repository's
;; .nodwyn/init.scm runs its whole check, its Go tests and its make targets
;; through such tools, and the harness counted shell-exec runs only, so the
;; plan's evidence, check-without and the plan's marks never saw them: on
;; commit-bench (dsl18m, 1e595e5, 2026-10-14) the end-of-turn evidence
;; message fired in every trusted attempt, mostly after a validate or go-test
;; run that had passed since the last edit (3422faf1: steps 3, 4, 6 and 8 said
;; not to have passed after validate passed at call 111; ADR 32, amended
;; 2026-10-14). A project may add its own.
(define tool-commands
  '(("validate" "make" ("validate") #f)
    ("go-test" "go" ("test") "pkg")
    ("run-make" "make" () "target")))

;; A call to one of tool-commands as the shell-exec call that runs its
;; command, made from the call's own arguments; any other call as it is.
(define (tool-call call)
  (let ((row (assoc (format "~a" (assoc-ref call 'name "")) tool-commands)))
    (if (not row)
        call
        (let* ((a (guard (e (else '())) (json-parse (format "~a" (assoc-ref call 'args "{}")))))
               (param (list-ref row 3))
               (value (and param (pair? a) (assoc-ref a param #f))))
          (list (cons 'name "shell-exec") (cons 'id (assoc-ref call 'id ""))
                (cons 'args (json-emit (list (cons "cmd" (list-ref row 1))
                                             (cons "args" (append (list-ref row 2) (if (string? value) (list value) '())))))))))))

;; The status a tool-commands tool's text says its command exited with: its
;; first line when that is "exit N" (dev/go.scm), else the first line that
;; reports a background run's exit ("p1: exited 0 (make validate)", the
;; validate tool), else #f.
(define (tool-status text)
  (let ((lines (string-split text "\n")))
    (or (let ((words (if (pair? lines) (string-split (car lines) " ") '())))
          (and (= (length words) 2) (equal? (car words) "exit")
               (let ((n (string->number (cadr words)))) (and (integer? n) n))))
        (let walk ((ls lines))
          (and (pair? ls)
               (or (let ((words (string-split (car ls) " ")))
                     (and (>= (length words) 3) (string-suffix? ":" (car words)) (equal? (cadr words) "exited")
                          (let ((n (string->number (caddr words)))) (and (integer? n) n))))
                   (walk (cdr ls))))))))

;; A result of one of tool-commands as shell-exec gives one: the status, and
;; the tool's text as what the command printed. It is tool output: it says
;; whether the run counts, and goes into no call.
(define (tool-result call result)
  (if (not (assoc (format "~a" (assoc-ref call 'name "")) tool-commands))
      result
      (let* ((text (format "~a" (assoc-ref result 'value "")))
             (status (tool-status text)))
        (list (cons 'ok (assoc-ref result 'ok #f))
              (cons 'value (json-emit (if status
                                          (list (cons "status" status) (cons "stdout" text))
                                          (list (cons "stdout" text)))))))))

;; A shell-exec call's command line, cmd and args joined; for a call to one of
;; tool-commands, the command it runs.
(define (command-line call)
  (let ((args (guard (e (else '())) (json-parse (assoc-ref (tool-call call) 'args "{}")))))
    (if (pair? args)
        (string-join (cons (format "~a" (assoc-ref args "cmd" ""))
                           (map (lambda (a) (format "~a" a)) (let ((l (assoc-ref args "args" '()))) (if (list? l) l '()))))
                     " ")
        "")))

(define (test-call? call)
  (and (equal? (assoc-ref (tool-call call) 'name) "shell-exec")
       (let ((line (command-line call)))
         (let any ((ws test-command-words))
           (and (pair? ws) (or (string-contains? line (car ws)) (any (cdr ws))))))))

(define (edit-call? call) (member (assoc-ref call 'name) '("fs-write" "fs-edit")))

;; (ran-ok? result), whether a test command ran, is defined by shell.scm,
;; which owns it: it reads the shell habit's plain-text results and counts a
;; run with failures as a run. The evidence loop below calls it by name.

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

;; Whether a path (repo-path of a call's) is one inside the repository: a
;; path left absolute or climbing out of it names a file the guard refuses the
;; model, and refuses the harness too, whose own call on it is a denial no
;; strategy can catch, which ends the turn (SWE-bench's django-11400, a write
;; to /tmp after a run; ADR 32, amended 2026-10-16). The harness reads and
;; measures only paths this accepts.
(define (repo-relative? path)
  (and (not (string-empty? path))
       (not (string-prefix? "/" path))
       (not (member ".." (string-split path "/")))))

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

;; Whether a command line's exit status may be another command's than its
;; tests': a pipe into another program (tail, grep) without pipefail, an ||
;; (|| true), or a $? the line itself reports (; echo exit=$?). Such a line
;; exits 0 whether its tests passed or not: on commit-bench (dsl18m,
;; 2026-10-14) plan steps 3-6 of 3422faf1 were marked passed from a run whose
;; tests had failed but which ended each in `| tail -n 8 || true`, and on
;; c80b38ac a step from `| tail; echo exit=$?` (ADR 32, amended 2026-10-14).
;; Quoted text is no operator: go test's -run 'TestA|TestB' pipes nothing.
(define (masked-run? call)
  (let ((line (command-line call)))
    (or (string-contains? line "$?")
        (let scan ((i 0) (quote-char #f))
          (and (< i (string-length line))
               (let ((ch (substring line i (+ i 1))))
                 (cond (quote-char (scan (+ i 1) (if (equal? ch quote-char) #f quote-char)))
                       ((member ch '("'" "\"")) (scan (+ i 1) ch))
                       ((equal? ch "|")
                        (or (and (< (+ i 1) (string-length line)) (equal? (substring line (+ i 1) (+ i 2)) "|"))
                            (not (string-contains? line "pipefail"))
                            (scan (+ i 1) #f)))
                       (else (scan (+ i 1) #f)))))))))

;; What a shell-exec result printed, its output and its errors.
(define (run-output result)
  (let ((v (guard (e (else #f)) (json-parse (format "~a" (assoc-ref result 'value ""))))))
    (if (pair? v)
        (string-append (format "~a" (assoc-ref v "stdout" "")) "\n" (format "~a" (assoc-ref v "stderr" "")))
        "")))

;; The number before a word in a run's summary line, "3 passed, 1 failed in
;; 0.4s" (pytest) or "Tests: 3 passed, 4 total" (jest): 3 for "passed"; #f
;; when the line has none.
(define (summary-count line word)
  (let walk ((ts (string-split line " ")))
    (cond ((or (null? ts) (null? (cdr ts))) #f)
          ((and (string->number (car ts))
                (equal? (let ((w (cadr ts))) (if (string-suffix? "," w) (substring w 0 (- (string-length w) 1)) w)) word))
           (string->number (car ts)))
          (else (walk (cdr ts))))))

(define (summary-counts? line words)
  (let any ((ws words))
    (and (pair? ws) (let ((n (summary-count line (car ws)))) (or (and n (> n 0)) (any (cdr ws)))))))

;; The lines of a run's output that show tests passing, whatever the
;; language: go test's "--- PASS: TestX" and "ok  <tab>package", pytest's
;; and jest's "3 passed", unittest's and Django's "OK", cargo's "test result:
;; ok.", rspec's "3 examples, 0 failures", and validate's summary (ADR 32,
;; amended 2026-10-16: only Go's and validate's were read, so a Python run
;; piped through tail was no run that passed).
(define (passing-lines output)
  (filter (lambda (l)
            (let ((t (string-trim l)))
              (or (string-contains? l "--- PASS: ") (string-prefix? "ok  \t" l) (string-prefix? "validate: PASS" l)
                  (and (summary-count t "passed") (> (summary-count t "passed") 0) (not (summary-counts? t '("failed" "failures" "error" "errors"))))
                  (equal? t "OK") (string-prefix? "OK (" t)
                  (string-prefix? "test result: ok." t)
                  (and (string-contains? t " 0 failures") (or (string-contains? t " examples,") (string-contains? t " example,"))))))
          (string-split output "\n")))

;; Whether a run's output shows a failure, whatever the language: go test's
;; "--- FAIL" and "FAIL" lines, a package that did not build or start, a
;; panic, validate's FAIL; pytest's and jest's "1 failed" or "1 error",
;; unittest's "FAILED (failures=1)", "FAIL:" and "ERROR:" lines, cargo's
;; "test result: FAILED", rspec's "2 failures".
(define (failing-output? output)
  (let any ((ls (string-split output "\n")))
    (and (pair? ls)
         (let ((l (string-trim (car ls))))
           (or (string-prefix? "--- FAIL" l) (string-prefix? "FAIL" l) (string-prefix? "panic: " l)
               (string-prefix? "validate: FAIL" l) (string-contains? l "[build failed]") (string-contains? l "[setup failed]")
               (string-prefix? "ERROR: " l) (string-prefix? "test result: FAILED" l)
               (summary-counts? l '("failed" "failures" "error" "errors"))
               (any (cdr ls)))))))

;; A run that passed, as the plan's evidence keeps it: (line . shown), its
;; command line and the lines of its output that show tests passing; or #f.
;; It passed when it exited 0, on the change and not on the copy of the last
;; commit (base-run?), and, when its status may be another command's
;; (masked-run?), when its output shows tests passing and none failing.
(define (run-entry call result)
  (and (equal? (assoc-ref call 'name) "shell-exec")
       (equal? (run-status result) 0)
       (not (base-run? call))
       (let ((shown (passing-lines (run-output result))))
         (and (or (not (masked-run? call))
                  (and (pair? shown) (not (failing-output? (run-output result)))))
              (cons (command-line call) (string-join shown "\n"))))))

(define (base-outcome result last)
  (let ((status (run-status result)))
    (if (and (equal? last 'failed) status (not (= status 0)))
        'compared
        last)))

;; The failures a test run's output reports: Go's "--- FAIL: TestX (0.00s)"
;; (a sub-test as TestX/case) and "FAIL<tab>package<tab>0.3s" (a package, as
;; "package <path>"), pytest's "FAILED path::test - why", unittest's
;; "FAIL: test (module.Class)" and "ERROR: ...", cargo's, jest's and rspec's,
;; each once, in the order the output gives them. They are what the loop compares across runs: the tests
;; the unchanged code fails are not the change's (failures-known?). Other
;; output gives none, and the comparison is not made.
(define (failure-line-name l)
  (let ((first-word (lambda (text)
                      (let ((words (string-split text " ")))
                        (and (pair? words) (not (string-empty? (car words))) (car words))))))
    (cond ((string-prefix? "--- FAIL: " l) (first-word (substring l 10 (string-length l))))
          ((string-prefix? "FAIL\t" l)
           (let* ((fields (string-split l "\t"))
                  (word (and (pair? (cdr fields)) (first-word (cadr fields)))))
             (and word (string-append "package " word))))
          ((string-prefix? "FAILED " l) (first-word (substring l 7 (string-length l))))
          ((string-prefix? "FAIL: " l)
           (let ((rest (string-trim (substring l 6 (string-length l))))) (and (not (string-empty? rest)) rest)))
          ((string-prefix? "ERROR: " l)
           (let ((rest (string-trim (substring l 7 (string-length l))))) (and (not (string-empty? rest)) rest)))
          ;; cargo's "test calc::subs ... FAILED", jest's "● suite › case",
          ;; rspec's "rspec ./spec/calc_spec.rb:12 # description".
          ((and (string-prefix? "test " l) (string-suffix? " ... FAILED" l))
           (first-word (substring l 5 (string-length l))))
          ((and (string-prefix? "● " l) (string-contains? l " › "))
           (string-trim (substring l 2 (string-length l))))
          ((string-prefix? "rspec ./" l) (first-word (substring l 6 (string-length l))))
          (else #f))))

(define (failure-names output)
  (let walk ((lines (string-split output "\n")) (out '()))
    (if (null? lines)
        (reverse out)
        (let ((name (failure-line-name (string-trim (car lines)))))
          (walk (cdr lines) (if (and name (not (member name out))) (cons name out) out))))))

(define (package-name? name) (string-prefix? "package " name))

;; The names in more that names lacks, in front of names.
(define (names-union more names)
  (append (filter (lambda (n) (not (member n names))) more) names))

;; Whether a run is narrowed to some tests (go test -run, pytest -k or a
;; node id): its failures are the ones asked for, not the suite's.
(define (narrowed-run? call)
  (let ((line (command-line call)))
    (or (string-contains? line " -run") (string-contains? line " -k ") (string-contains? line "::")
        (string-contains? line " -m "))))

;; The words a failing test goes by in a text: its name, the test in a
;; pytest node id, the test a Go sub-test belongs to, the first word of a
;; unittest line. Short ones are no mark of a test.
(define (test-name-words name)
  (filter (lambda (w) (>= (string-length w) 4))
          (list name
                (car (reverse (string-split name "::")))
                (car (string-split name "/"))
                (car (string-split name " ")))))

;; The failing tests of names (failure-names of a run), split into the ones
;; also failing on the unchanged code (base: those of the runs there and
;; before any edit) and the others: (known . fresh), packages left out. A
;; test is the unchanged code's to answer for only if nothing makes it the
;; turn's: the request names it, the plan does, or a file the turn wrote
;; (written) mentions it. A test that failed on the copy of the last commit
;; with the model's own test laid over it, or that the task asks to fix,
;; failing again says the change is not done.
(define (failure-split names base request written)
  (let* ((texts (cons request
                      (cons (guard (e (else "")) (string-join (plan-steps) "\n"))
                            (map (lambda (p) (guard (e (else "")) (format "~a" (fs-read p)))) written))))
         (own? (lambda (name)
                 (let any ((ws (test-name-words name)))
                   (and (pair? ws)
                        (or (let in ((ts texts)) (and (pair? ts) (or (string-contains? (car ts) (car ws)) (in (cdr ts)))))
                            (any (cdr ws)))))))
         (tests (filter (lambda (n) (not (package-name? n))) names)))
    (let split ((ts tests) (known '()) (fresh '()))
      (cond ((null? ts) (cons (reverse known) (reverse fresh)))
            ((and (member (car ts) base) (not (own? (car ts)))) (split (cdr ts) (cons (car ts) known) fresh))
            (else (split (cdr ts) known (cons (car ts) fresh)))))))

;; Whether every failure of a run is the unchanged code's: it failed at least
;; one test, each is known (failure-split), and each package it failed
;; failed there too (a package that fails to build, or panics, names no test).
(define (failures-known? names base request written)
  (and (pair? names) (pair? base)
       (let ((split (failure-split names base request written)))
         (and (pair? (car split)) (null? (cdr split))
              (let all ((ns names))
                (or (null? ns)
                    (and (or (not (package-name? (car ns))) (member (car ns) base))
                         (all (cdr ns)))))))))

;; The failing-run reminder for a run's failures: the one it always was, or,
;; when some of the failures are the unchanged code's, the same naming them.
(define (failing-reminder names base request written)
  (let ((split (if (pair? base) (failure-split names base request written) (cons '() '()))))
    (if (null? (car split))
        finish-failing-message
        (finish-failing-known-message (car split) (cdr split)))))

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
;; the whole check has passed since the last edit (a passing call, or one of
;; the runs that passed, run-entry's), a report that cites one of them stands
;; on that run.
(define whole-check-words '("make validate" "go run ./scripts/validate" "make check" "make test" "make ci" "make verify"))

(define (whole-check? call)
  (let ((line (command-line call)))
    (let any ((ws whole-check-words))
      (and (pair? ws) (or (string-contains? line (car ws)) (any (cdr ws)))))))

(define (whole-check-passed? passed)
  (let any ((ps passed))
    (and (pair? ps)
         (or (let w ((ws whole-check-words)) (and (pair? ws) (or (string-contains? (car (car ps)) (car ws)) (w (cdr ws)))))
             (any (cdr ps))))))

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
;; both (ADR 32, amended 2026-10-16).
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
;; such reminders on commit-bench; ADR 32, amended 2026-10-16).
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
         (or (string-contains? text p) (string-contains? text name) (plan-path-starts? path)))))

;; Whether a plan step's file starts path: a test step for a scenario names
;; the front of its file (test/exfil/scenarios/019), the rest being the
;; words the file is called by (ADR 32, amended 2026-10-16). Only a path
;; through a scenario folder (planspec.scm): the front of any file's name
;; would make its backup (calc.py.orig) the plan's.
(define (plan-path-starts? path)
  (let any ((ss (if (list? plan-spec) plan-spec '())))
    (and (pair? ss)
         (let ((front (assoc-ref (car ss) 'path "")))
           (or (and (string? front) (plan-language-scenario-dir front) (string-prefix? front path))
               (any (cdr ss)))))))

;; Whether a repository path is a test file by its name or its folder:
;; x_test.go, test_x.py, x.test.js, x.spec.ts, or a file under a test, tests
;; or __tests__ folder.
(define (test-file-path? path)
  (let* ((parts (string-split path "/"))
         (base (if (null? parts) "" (car (reverse parts)))))
    (or (string-contains? base "_test.") (string-contains? base ".test.") (string-contains? base ".spec.")
        (string-prefix? "test_" base)
        (let any ((dirs (if (null? parts) '() (cdr (reverse parts)))))
          (and (pair? dirs) (or (pair? (member (car dirs) '("test" "tests" "__tests__"))) (any (cdr dirs))))))))

;; An existing test whose expectation the turn changes is asked about as the
;; edit is made, once for each test file, and the edit is never refused. A
;; round that edited test files (edited: their paths) not yet told (told) is
;; set against HEAD with change-removals, which reads only the lines HEAD
;; has: a test the turn added, or a case it added to a table, has none to
;; name. The lines it names for the fresh files go in one message
;; (edited-expectations-message); the files that had any, and the lines, are
;; returned as (files . lines), the files to be added to told and the lines
;; to the rows the answer is checked for (edit-answers). The finish review
;; still names every such line the answer did not support when the latest run
;; passed (finish-review-points): it is the last place to ask.
;; On 94f5cac4 the review named the wrong count row among six lines, as one
;; of four points, some twenty rounds after the edits, and the answer
;; defended all six (ADR 32, amended 2026-10-16).
(define (test-edit-nudge request edited told)
  (let ((fresh (let keep ((ps edited) (out '()))
                 (cond ((null? ps) (reverse out))
                       ((or (member (car ps) told) (member (car ps) out)) (keep (cdr ps) out))
                       (else (keep (cdr ps) (cons (car ps) out)))))))
    (if (or lean-quiet turn-stopping (null? fresh) (not (host-primitive? "change-removals")))
        (cons '() '())
        (let* ((removed (guard (e (else (log-note (string-append "change-removals failed: " (format "~a" (assoc-ref e 'message ""))))
                                        '()))
                          (change-removals request)))
               (lines (filter (lambda (l) (member (assoc-ref l "path" "") fresh))
                              (assoc-ref removed "expectations" '()))))
          (if (null? lines)
              (cons '() '())
              (begin (add-message (edited-expectations-message lines))
                     (cons (filter (lambda (p) (pair? (filter (lambda (l) (equal? (assoc-ref l "path" "") p)) lines)))
                                   fresh)
                           lines)))))))

;; The answer to the question above is read, not weighed (edit-support): a
;; row is answered when the reply quotes the request, word for word, in a
;; sentence that shares distinctive words with the row's input and the
;; message it expects. On 94f5cac4 (dsl22) the question fired three times and
;; the model went on: it answered with the request's rule for a skipped
;; argument, which says nothing of a count of 0, and kept the rewritten row
;; `(fs-read "big.txt" 1 0)` "count must be at least 1" (ADR 32, amended
;; 2026-10-16). Nothing is reverted or refused. asked is the rows the
;; question named, each (state . line) with the state asked, firm (the
;; firmer message was said) or ok (answered, or restored). A reply that
;; quotes the request and answers no row gets one firmer message for it
;; (edited-expectations-firm-message), once for each row; a reply that quotes
;; nothing is no answer yet. The reply that ends the turn (final) is read for
;; the rows it supports, and says nothing more: the rows it leaves are the
;; finish review's first point, which is firm enough. Returns (asked . said):
;; the rows as they are now, and whether it added the message.
(define (edit-answers request content asked final)
  (let ((open (filter (lambda (a) (not (eq? (car a) 'ok))) asked)))
    (if (or (null? open) turn-stopping (string-empty? (string-trim content)) (not (host-primitive? "edit-support")))
        (cons asked #f)
        (let* ((removed (guard (e (else (log-note (string-append "change-removals failed: " (format "~a" (assoc-ref e 'message ""))))
                                        #f))
                          (change-removals request)))
               ;; A row the model restored is no longer a change: answered.
               (now (if removed (assoc-ref removed "expectations" '()) #f))
               (present? (lambda (row)
                           (or (not now)
                               (let any ((ls now)) (and (pair? ls) (or (edit-same-row? row (car ls)) (any (cdr ls))))))))
               (still (filter (lambda (a) (present? (cdr a))) open))
               (verdicts (if (null? still)
                             '()
                             (map cons (map cdr still)
                                  (guard (e (else (log-note (string-append "edit-support failed: " (format "~a" (assoc-ref e 'message ""))))
                                                  (map (lambda (a) '()) still)))
                                    (edit-support (map cdr still) request content)))))
               (said '()))
          (let ((next (filter (lambda (a) a)
                              (map (lambda (a)
                                     (let ((v (assoc (cdr a) verdicts)))
                                       (cond ((eq? (car a) 'ok) a)
                                             ((not v) #f)
                                             ;; A row with no word to judge it by is left to
                                             ;; the finish review's other point.
                                             ((equal? (assoc-ref (cdr v) "status" "") "unchecked") #f)
                                             ((not (equal? (assoc-ref (cdr v) "status" "") "unsupported")) (cons 'ok (cdr a)))
                                             ((and (eq? (car a) 'asked) (not final) (assoc-ref (cdr v) "quoted" #f))
                                              (set! said (append said (list (cdr v))))
                                              (cons 'firm (cdr a)))
                                             (else a))))
                                   asked))))
            (when (pair? said) (add-message (edited-expectations-firm-message said)))
            (cons next (pair? said)))))))

(define (edit-same-row? a b)
  (and (equal? (assoc-ref a "path" "") (assoc-ref b "path" ""))
       (equal? (assoc-ref a "text" "") (assoc-ref b "text" ""))))

;; Source files, by extension, as the finish review reads which directories a
;; change touches (changed-dirs); a project may redefine the list.
(define source-extensions
  '(".go" ".py" ".js" ".jsx" ".ts" ".tsx" ".rs" ".java" ".kt" ".rb" ".php" ".c" ".cc" ".cpp" ".h" ".cs"))

(define (source-path? path)
  (and (not (test-file-path? path))
       (let any ((exts source-extensions))
         (and (pair? exts) (or (string-suffix? (car exts) path) (any (cdr exts)))))))

;; The directory of a repository path: "" for a file at the root.
(define (path-dir path)
  (let ((parts (string-split path "/")))
    (if (or (null? parts) (null? (cdr parts)))
        ""
        (string-join (reverse (cdr (reverse parts))) "/"))))

;; The directories of the source files the turn changed (touched).
(define (changed-dirs touched)
  (let walk ((ps touched) (out '()))
    (cond ((null? ps) (reverse out))
          ((and (source-path? (car ps)) (not (member (path-dir (car ps)) out)))
           (walk (cdr ps) (cons (path-dir (car ps)) out)))
          (else (walk (cdr ps) out)))))

;; How much of the changed directories a passing command runs: all of them
;; for go test ./... and the repository's whole-check words, else the ones
;; its line names ("go test ./internal/prim/"), and one more if it is not
;; narrowed to some tests (-run, -k).
(define (command-reach call dirs)
  (let* ((line (command-line call))
         (wide (or (string-contains? line "./...")
                   (let any ((ws whole-check-words)) (and (pair? ws) (or (string-contains? line (car ws)) (any (cdr ws))))))))
    (+ (* 2 (if wide
                (+ (length dirs) 1)
                (length (filter (lambda (d) (and (not (string-empty? d)) (string-contains? line d))) dirs))))
       (if (narrowed-run? call) 0 1))))

;; The passing call that runs the most of what the turn changed, the newest
;; of those that tie: the check check-without takes apart. The last passing
;; call was the exfiltration suite alone on
;; commit-bench 855e90e7, which ran none of the changed packages' tests, so
;; check-without named four hunks no test covered and the run spent 53 calls
;; on revert experiments (ADR 32, amended 2026-10-16, the original tests).
;; calls: the passing calls since the last edit, newest first.
(define (broadest-passing calls touched)
  (and (pair? calls)
       (let ((dirs (changed-dirs touched)))
         (let best ((cs (cdr calls)) (top (car calls)) (score (command-reach (car calls) dirs)))
           (if (null? cs)
               top
               (let ((s (command-reach (car cs) dirs)))
                 (if (> s score) (best (cdr cs) (car cs) s) (best (cdr cs) top score))))))))

;; The program and arguments a shell-exec call ran, as the primitives that run
;; a check again take them: (cmd . args), or #f.
(define (call-argv call)
  (let* ((a (guard (e (else '())) (json-parse (format "~a" (assoc-ref call 'args "{}")))))
         (cmd (if (pair? a) (format "~a" (assoc-ref a "cmd" "")) "")))
    (and (not (string-empty? cmd))
         (cons cmd (let ((l (assoc-ref a "args" '()))) (if (list? l) (map (lambda (x) (format "~a" x)) l) '()))))))

;; The words of a test file's path that a command naming the file would
;; contain: the path, its name without the extension, and the folders above
;; it that say little ("tests", "src") left out. Short ones are no mark.
(define generic-test-folders '("test" "tests" "testing" "__tests__" "spec" "src" "lib" "internal" "pkg" "cmd"))

(define (test-path-words path)
  (let* ((parts (string-split path "/"))
         (base (if (null? parts) path (car (reverse parts))))
         (stem (car (string-split base ".")))
         (folders (if (or (null? parts) (null? (cdr parts))) '() (reverse (cdr (reverse parts))))))
    (filter (lambda (w) (and (>= (string-length w) 4) (not (member w generic-test-folders))))
            (cons path (cons stem folders)))))

;; Whether a passing run runs the test files in paths: it is a test run, and it
;; either names one of them (its path, its name, a folder it is in) or is not
;; narrowed to anything (`pytest`, `go test ./...`, `make test`).
(define (run-covers-tests? call paths)
  (and (test-call? call)
       (let ((line (command-line call)))
         (or (string-contains? line "./...")
             (whole-check? call)
             (let any ((ps paths))
               (and (pair? ps)
                    (or (let word ((ws (test-path-words (car ps))))
                          (and (pair? ws) (or (string-contains? line (car ws)) (word (cdr ws)))))
                        (any (cdr ps)))))))))

;; The passing call to run again with the edited test files as the last
;; commit had them: one that runs them, the newest, a run not narrowed to
;; some tests before one that is. calls: the passing calls since the last
;; edit, newest first.
(define (original-check calls paths)
  (let ((covering (filter (lambda (c) (run-covers-tests? c paths)) calls)))
    (cond ((null? covering) #f)
          (else (let ((wide (filter (lambda (c) (not (narrowed-run? c))) covering)))
                  (car (if (pair? wide) wide covering)))))))

;; The existing tests of the test files the turn edited that fail when those
;; files are as the last commit had them, with the check the turn passed
;; (original-tests), as a list of alists of path and tests. The turn's own
;; tests are not among them (its new test files are taken away, and the edited
;; files are the last commit's). It runs the check the turn itself passed, so
;; it works for any language whose runs print their failures (ADR 32,
;; amended 2026-10-16: until then it ran Go's tests alone, and the Python
;; tasks that rewrote an existing test were asked nothing). passing-all: the
;; calls that passed since the last edit, newest first. Without a passing run
;; that covers the edited test files, the log says so and the earlier lines
;; of the finish review stay.
(define (original-test-failures touched passing-all)
  (let ((paths (let keep ((ps touched) (out '()))
                 (cond ((null? ps) (reverse out))
                       ((and (test-file-path? (car ps)) (not (member (car ps) out))) (keep (cdr ps) (cons (car ps) out)))
                       (else (keep (cdr ps) out))))))
    (cond ((null? paths) '())
          ((not (host-primitive? "original-tests")) '())
          (else
           (let* ((call (original-check passing-all paths))
                  (argv (and call (call-argv call))))
             (cond ((not argv)
                    (log-note "original-tests: not run, no run that passed since the last edit runs the test files you edited")
                    '())
                   (else
                    (let ((result (guard (e (else (log-note (string-append "original-tests failed: " (format "~a" (assoc-ref e 'message ""))))
                                                    '()))
                                    (original-tests (car argv) (cdr argv) paths))))
                      (for-each (lambda (why) (log-note (string-append "original-tests: " (format "~a" why))))
                                (assoc-ref result "unrun" '()))
                      (assoc-ref result "failures" '())))))))))

;; The tests a test run that failed shows failing that the last commit does
;; not fail (original-tests with no edited file: the same command, with every
;; changed file as the last commit had it), as a list of names. On SWE-bench's
;; psf-requests-2931 the change broke an existing test, the model ran the
;; whole file, saw 81 failures it put down to the environment (an HTTP test
;; server), and one of them was its own (ADR 32, amended 2026-10-16).
(define (regression-failures call)
  (let ((argv (call-argv call)))
    (if (not argv)
        '()
        (let ((result (guard (e (else (log-note (string-append "original-tests failed: " (format "~a" (assoc-ref e 'message ""))))
                                        '()))
                        (original-tests (car argv) (cdr argv) '()))))
          (for-each (lambda (why) (log-note (string-append "original-tests: " (format "~a" why))))
                    (assoc-ref result "unrun" '()))
          (let walk ((fs (assoc-ref result "failures" '())) (out '()))
            (if (null? fs) (reverse out) (walk (cdr fs) (append (reverse (assoc-ref (car fs) "tests" '())) out))))))))

;; The finish review asks for a test only a few times: at most
;; finish-ask-limit items of a kind are named, and an item named in one
;; review of a run is not named in a later one (finish-asked: their keys,
;; the run's, like finish-reviews). The items past the limit are not named
;; and not asked later either: the model answered the review once.
(define finish-ask-limit 3)
(define finish-asked '())

;; (finish-ask key-of items): the items not asked about yet, as (shown .
;; omitted): the first finish-ask-limit of them, in order, and how many more
;; there were. All of them count as asked from now on.
(define (finish-ask key-of items)
  (let* ((fresh (filter (lambda (i) (not (member (key-of i) finish-asked))) items))
         (shown (let take ((is fresh) (n finish-ask-limit) (out '()))
                  (if (or (null? is) (<= n 0)) (reverse out) (take (cdr is) (- n 1) (cons (car is) out))))))
    (set! finish-asked (append (map key-of fresh) finish-asked))
    (cons shown (- (length fresh) (length shown)))))

;; A part of the change check-without found, by where it is and how big,
;; and its size.
(define (part-key p)
  (format "part ~a ~a +~a -~a" (assoc-ref p "path" "") (assoc-ref p "kind" "") (assoc-ref p "added" 0) (assoc-ref p "removed" 0)))

(define (part-size p)
  (+ (assoc-ref p "added" 0) (assoc-ref p "removed" 0)))

;; A dropped condition, by the function and the operand.
(define (dropped-key d)
  (format "dropped ~a ~a ~a" (assoc-ref d "path" "") (assoc-ref d "function" "") (assoc-ref d "operand" "")))

;; The finish review: what an unattended turn's change lacks, gathered when
;; the turn first ends with a change and no reminder of its evidence is due,
;; and said in one message (finish-review-message), once a turn: the new
;; files git does not track (new, which check-without's parts leave out
;; rather than name them twice), the plan's steps without their evidence
;; (planspec.scm), the parts of the change the passing check does not test
;; (check-without, run when passing is the check that passed since the last
;; edit), and the commands the report cites that have not run since the last
;; edit. They were four messages, each once a turn, and each answer, an edit
;; or a script, re-armed the reminders before them (ADR 32, amended
;; 2026-10-15).
;; When the latest run since the last edit passed (quiet: the model's own
;; check did not notice), it also names what the change removed
;; (change-removals): definitions HEAD had that are gone from their package,
;; and expectations of existing tests removed or rewritten. On commit-bench
;; a deleted helper the hidden test called failed dsl16's and Pi's builds
;; (67a1f950), and Pi's rewritten and deleted cases failed three tasks
;; (94f5cac4, ef04de9c, 5040f9fe), each after its own tests had passed.
;; With it come the parts of a condition a rewritten Go function no longer
;; checks (change-dropped-conditions): dsl19 dropped the judge's Enabled()
;; on c80b38ac, and no test had a case where it was false.
;; And it names what the lines the change adds to documents and comments
;; state that nothing in the turn showed (change-claims, given the turn's
;; runs as shown keeps them and the files it edited by hand): a run's result
;; or figures, an amendment date, a paragraph pasted twice, entries added by
;; hand to a generated file.
;; And it names the existing tests the turn edited that fail when their files
;; are as the last commit had them (original-test-failures): what a
;; grader runs, and what a rewritten want string hides (5040f9fe).
;; The tests it asks for are bounded: the parts the check passed without, the
;; plan's test steps and the dropped conditions are each named at most
;; finish-ask-limit at a time, and an item asked about once in a run is not
;; asked about again (finish-ask). It asked, until 2026-10-16, for the lines
;; of the change no test executed too; on commit-bench 6e18b7ba (dsl22) that
;; point, with a coverage run its message asked for, took 62 of the run's 167
;; calls and 1.12M of its 2.38M cost-eq, in tests for branches of the change
;; and the model's own defensive code (ADR 32, amended 2026-10-16).
(define (finish-review-points request reply new passing quiet ran ran-all checked passed shown hand-edits made touched asked-edits passing-all failing-call)
  (let* ((plan (if (and plan-language (pair? plan-spec))
                   (let ((check (plan-language-check passed)))
                     (plan-language-mark (cadr check))
                     (car check))
                   '()))
         (found (if (and passing (check-without?))
                    (filter (lambda (p) (not (member (assoc-ref p "path" "") new))) (untested-parts passing))
                    '()))
         (asked (finish-ask part-key (sort found (lambda (a b) (> (part-size a) (part-size b))))))
         (parts (car asked))
         (stale (stale-citations (format "~a" (assoc-ref reply 'content "")) ran ran-all checked))
         ;; The rows the question at the edit named and the answer did not
         ;; support, still changed: the review's first point. They are looked
         ;; for even when the latest run failed, since the question was asked.
         (open-edits (map cdr (filter (lambda (a) (not (eq? (car a) 'ok))) asked-edits)))
         (removed (if (and (or quiet (pair? open-edits)) (host-primitive? "change-removals"))
                      (guard (e (else (log-note (string-append "change-removals failed: " (format "~a" (assoc-ref e 'message ""))))
                                      '()))
                        (change-removals request))
                      '()))
         (defs (if quiet (assoc-ref removed "definitions" '()) '()))
         (current (assoc-ref removed "expectations" '()))
         (unsupported (filter (lambda (l) (let any ((os open-edits)) (and (pair? os) (or (edit-same-row? l (car os)) (any (cdr os))))))
                              current))
         ;; The others are named when the latest run passed, but for the rows
         ;; whose answer was read and supported.
         (lines (if quiet
                    (filter (lambda (l)
                              (not (or (member l unsupported)
                                       (let any ((as asked-edits))
                                         (and (pair? as) (or (and (eq? (car (car as)) 'ok) (edit-same-row? l (cdr (car as)))) (any (cdr as))))))))
                            current)
                    '()))
         (original (if quiet (original-test-failures touched passing-all) '()))
         (regressed (if (and (not quiet) failing-call) (regression-failures failing-call) '()))
         (dropped (car (finish-ask dropped-key
                                   (if (and quiet (host-primitive? "change-dropped-conditions"))
                                       (guard (e (else (log-note (string-append "change-dropped-conditions failed: " (format "~a" (assoc-ref e 'message ""))))
                                                       '()))
                                         (change-dropped-conditions request))
                                       '()))))
         (claims (if (host-primitive? "change-claims")
                     (guard (e (else (log-note (string-append "change-claims failed: " (format "~a" (assoc-ref e 'message ""))))
                                     '()))
                       (change-claims request shown hand-edits generated-files))
                     '())))
    (append (if (pair? unsupported) (list (finish-unsupported-edits-message unsupported original)) '())
            (if (pair? new) (list (finish-new-files-message new made)) '())
            (if (pair? plan) (list (plan-language-unmet-message plan)) '())
            (if (pair? parts) (list (finish-untested-parts-message (command-line passing) parts (cdr asked))) '())
            (if (pair? stale) (list (finish-stale-citations-message stale)) '())
            (if (pair? defs) (list (finish-removed-definitions-message defs)) '())
            (if (pair? lines) (list (finish-removed-expectations-message lines)) '())
            (if (and (pair? original) (null? unsupported)) (list (finish-original-tests-message original)) '())
            (if (pair? regressed) (list (finish-regressed-tests-message regressed)) '())
            (if (pair? dropped) (list (finish-dropped-conditions-message dropped)) '())
            (if (pair? claims) (list (finish-unshown-claims-message claims)) '()))))

;; Whether the host offers a primitive hidden from the model, as the analyses
;; of a git work tree are.
(define (host-primitive? name)
  (guard (e (else #f)) (primitive-info name) #t))

;; A repository's whole check run through the shell counts as its validate
;; tool's run. nodwyn's .nodwyn/init.scm reminds a turn that changed files
;; and did not validate after its last change, comparing (validation-state)
;; with validated-state, which only its validate tool sets; a turn that ran
;; make validate in the shell was told "validate has not run" (commit-bench,
;; A c97f9a41: twice, after make validate ran at the calls before each). The
;; benchmark runs the harness against old snapshots whose init.scm it cannot
;; change, so the harness sets validated-state as the tool does, after any
;; run of make validate that ran to an exit, in a repository that defines
;; both; elsewhere this does nothing.
;;
;; make validate is go run ./scripts/validate, and a model runs either: the
;; gate did not count the second, and asked for validate again (commit-bench,
;; dsl16r, 3422faf1; ADR 32, amended 2026-10-16). Both are validate-commands,
;; and a run of either has every effect the tool's has: it is a test run
;; (test-command-words), the whole check (whole-check-words), no check to take
;; apart (evidence-loop) and the repository's validate (validated-run).
(define validate-commands '("make validate" "go run ./scripts/validate"))

(define (validate-command? cmd)
  (let any ((vs validate-commands))
    (and (pair? vs)
         (or (equal? cmd (car vs)) (string-prefix? (string-append (car vs) " ") cmd) (any (cdr vs))))))

;; Whether a call runs a validate command: the validate tool (tool-call), or
;; a shell-exec or shell-start line with one among its commands.
(define (validate-call? call)
  (let any ((cs (call-commands (tool-call call))))
    (and (pair? cs) (or (validate-command? (car cs)) (any (cdr cs))))))

;; A validate run that ran to an exit.
(define (validate-exit? call result)
  (and (run-status result) (validate-call? call)))

;; Whether a round's calls may leave files behind that are not the model's:
;; a validate run, or a tool the repository registers to run a command
;; (tool-commands).
(define (tool-round? calls)
  (let any ((cs calls))
    (and (pair? cs)
         (or (assoc (format "~a" (assoc-ref (car cs) 'name "")) tool-commands)
             (and (member (assoc-ref (car cs) 'name) '("shell-exec" "shell-start")) (validate-call? (car cs)))
             (any (cdr cs))))))

(define (validated-run call result)
  (when (validate-exit? call result)
    (guard (e (else #f))
      (set! validated-state (validation-state)))))

;; Files a validate run, or a tool of the repository, leaves untracked are
;; its output, not the model's change. validate regenerated the untracked
;; docs/expert/expert.pdf on commit-bench (dsl20r, 6e18b7ba); the model
;; deleted it, the repository's reminder counted the deletion as a change
;; since validate and asked for validate again, which made the file again:
;; about 100k tokens in two builds, and in a third the last validate left the
;; 1 MB file in the patch (ADR 32, amended 2026-10-16). So the loop keeps what
;; the runs made (validate-made, in evidence-loop) and the repository as
;; validate left it (validate-print), says what a validate run left in its
;; own result, and sets validated-state again (validate-settle) when the
;; only thing that moved is that some of those files are gone.
;;
;; The untracked paths in a (git-state).
(define (untracked-paths state)
  (map git-state-path (filter (lambda (l) (string-prefix? "?? " l)) state)))

;; The files untracked in now that were not in before, the model's own writes
;; (written: the paths the file tools wrote) left out: what the runs between
;; made. before is #f when no run could have started, now where there is no
;; git.
(define (files-made before now written)
  (if (and before now)
      (let ((was (untracked-paths before)))
        (filter (lambda (p) (and (not (member p was)) (not (git-shows? written (list (string-append "?? " p))))))
                (untracked-paths now)))
      '()))

;; The repository as validate saw it: git's status lines and the diff of the
;; tracked files; #f where there is no git.
(define (repo-print)
  (let ((state (git-state)))
    (and state (list state (guard (e (else #f)) (format "~a" (git-diff ".")))))))

;; The status lines but for the untracked files in made.
(define (state-without state made)
  (filter (lambda (l) (not (and (string-prefix? "?? " l) (member (git-state-path l) made)))) state))

;; print, the repository as the latest validate run left it, kept in step
;; with the repository: when the only difference is untracked files in made
;; (one gone, or another made since), validate still stands for the
;; repository, so validated-state is set to what the repository is now.
;; Any other difference is a change since validate, and print is #f from then
;; on. Nothing is done while made is empty or the status is as print has it.
(define (validate-settle made print)
  (let ((now (and print (pair? made) (git-state))))
    (cond ((not now) print)
          ((equal? now (car print)) print)
          ((not (equal? (state-without now made) (state-without (car print) made))) #f)
          (else
           (let ((diff (guard (e (else #f)) (format "~a" (git-diff ".")))))
             (if (and diff (equal? diff (cadr print)))
                 (begin (guard (e (else #f)) (set! validated-state (validation-state)))
                        (list now diff))
                 #f))))))

;; The generated files a repository declares, as rows (path command): a
;; trusted .nodwyn/init.scm may list those whose head does not say so (Code
;; generated, Generated by, a "source" address), with the command that makes
;; each. change-claims names entries added to one by hand that no later
;; command made or fetched.
(define generated-files '())

;; The turn's commands as change-claims reads them, newest first: each
;; (line status output), its command line ("" for a tool that runs none),
;; its exit status (#f when it did not run to an exit, or ran on the copy of
;; the last commit), and the start and end of what it printed. The file
;; tools and those that only look are left out: their answers show the
;; model's own text, a date it wrote among it.
(define shown-max 200)
(define shown-head 2000)
(define shown-tail 8000)

(define (shown-entry c0 c r0 r)
  (and (not (edit-call? c0))
       (not (look-call? c0))
       (not (member (assoc-ref c0 'name "") '("plan-write" "plan-step")))
       (let* ((text (if (equal? (assoc-ref c0 'name "") "shell-exec")
                        (run-output r0)
                        (format "~a" (assoc-ref r0 'value ""))))
              (n (string-length text))
              (sample (if (> n (+ shown-head shown-tail))
                          (string-append (substring text 0 shown-head) "\n…\n" (substring text (- n shown-tail) n))
                          text)))
         (list (command-line c) (and (not (base-run? c)) (run-status r)) sample))))

(define (shown-add shown entry)
  (if (not entry)
      shown
      (let ((all (cons entry shown)))
        (if (> (length all) shown-max) (reverse (cdr (reverse all))) all))))

;; The files the turn edited by hand, each with the command lines run after
;; its last edit: (path . lines).
(define (hand-edits-add edits call result)
  (cond ((and (edit-call? call) (assoc-ref result 'ok #f) (not (scratch-name call)))
         (let ((path (repo-path call)))
           (cons (list path) (filter (lambda (e) (not (equal? (car e) path))) edits))))
        (else edits)))

(define (hand-edits-ran edits line)
  (if (string-empty? line)
      edits
      (map (lambda (e) (cons (car e) (append (cdr e) (list line)))) edits)))

;; Whether a failed test run shows that tests ran: a test it names failed,
;; a test or package passed, or a package says FAIL with a time of its own
;; (not [build failed]). Such a run is not one that "did not run": its
;; failures are the tests', and finish-unrunnable-message is for a command
;; that never got to them (a missing program, a package that does not build).
(define (tests-ran? result)
  (let ((out (run-output result)))
    (or (pair? (filter (lambda (n) (not (package-name? n))) (failure-names out)))
        (pair? (passing-lines out))
        (let any ((ls (string-split out "\n")))
          (and (pair? ls)
               (or (and (string-prefix? "FAIL\t" (car ls))
                        (not (string-contains? (car ls) "[build failed]"))
                        (not (string-contains? (car ls) "[setup failed]")))
                   (any (cdr ls))))))))

;; How many lines a file's changes against the last commit hold.
(define (diff-size path)
  (guard (e (else 0))
    (length (filter (lambda (l) (and (or (string-prefix? "+" l) (string-prefix? "-" l))
                                     (not (string-prefix? "+++" l)) (not (string-prefix? "---" l))))
                    (string-split (format "~a" (git-diff path)) "\n")))))

;; The size of the changes (diff-size) to each file a round's edit calls
;; write, before they are made: (path . size). Documents and scratch files are
;; left out. Nothing is measured until the turn has run a command: an edit
;; takes part of the model's own change out to see a test fail only after a
;; test has run, and each measure is a git call.
(define (edit-sizes calls)
  (map (lambda (c) (cons (repo-path c) (diff-size (repo-path c))))
       (filter (lambda (c) (and (edit-call? c) (not (scratch-name c)) (repo-relative? (repo-path c)) (not (doc-path? (repo-path c))))) calls)))

;; Whether an edit call, made when the file's changes were sizes (edit-sizes),
;; left the file closer to the last commit: the model took a part of its own
;; change out, as a revert experiment does.
(define (shrinking-edit? call sizes)
  (let ((before (assoc (repo-path call) sizes)))
    (and before (< (diff-size (repo-path call)) (cdr before)))))

(define (evidence-loop request)
  ;; passing: the latest check or test call that exited 0 since the last
  ;; edit, for check-without. ran: the command lines run since the last edit,
  ;; and every program the turn ran, for the report's citations.
  ;; ran-all: the commands run in the turn; base-ran: those run on the copy
  ;; of the last commit, which no edit makes stale; started-ids and
  ;; started-calls: the background runs started since the last edit
  ;; (started-call).
  ;; passed: the runs that passed since the last edit (run-entry);
  ;; review-made: whether the finish review (finish-review-points) was made,
  ;; once a turn, after which an answer ends the turn. shown: the turn's
  ;; commands and what they printed (shown-entry); hand-edits: the files the
  ;; turn edited by hand and the commands run since (hand-edits-add).
  ;; tests-edited: the test files the latest round's calls wrote; tests-told:
  ;; the test files asked about once already (test-edit-nudge); edit-asked:
  ;; the rows that question named and what became of each (edit-answers).
  ;; validate-made: the untracked files the turn's validate runs and tools
  ;; left (validate-settle), validate-print the repository as the latest
  ;; validate run left it, tool-before the status before the latest round
  ;; that started such a run, and exited the validate run a round saw exit.
  ;; base-fails: the failing tests of the unchanged code the turn has seen
  ;; (failure-names of its runs before any edit and on the copy of the last
  ;; commit), last-fails the failing tests of the latest run since the last
  ;; edit, hinted the failures the model was last told were the unchanged
  ;; code's.
  ;; passing-all: the calls that passed since the last edit (passing is the
  ;; latest of them), newest first, for the broadest of them
  ;; (broadest-passing). sizes: the size of the changes to the files a round's
  ;; edits write, before the round; shrank: whether the latest edit took part
  ;; of the change out (shrinking-edit?), which makes the next failing run a
  ;; revert experiment, no sign that tests do not run.
  (let ((before (git-state)) (passing #f) (passing-all '()) (ran '()) (ran-all '()) (base-ran '()) (started-ids '()) (started-calls '())
        (passed '()) (review-made #f) (shown '()) (hand-edits '()) (tests-edited '()) (tests-told '()) (edit-asked '())
        (validate-made '()) (validate-print #f) (tool-before #f) (exited #f)
        (base-fails '()) (last-fails '()) (hinted '()) (sizes '()) (shrank #f) (failing-call #f))
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
               (scripts '()) (written '()) (excused #f) (reading 0) (outcome 'none)
               (told #f) (unchanged-told #f))
      (let ((calls (assoc-ref reply 'tool-calls '())))
        (if (pair? calls)
            (let walk ((cs calls)
                       (rs (begin (when (tool-round? calls) (set! tool-before (git-state)))
                                  (set! sizes (if (pair? ran-all) (edit-sizes calls) '()))
                                  (run-calls calls)))
                       (edited edited) (tested tested) (failed failed-runs)
                       (scripts scripts) (written written) (outcome outcome) (told told))
              (cond ((pair? cs)
                     (let* ((c0 (car cs)) (r0 (car rs))
                            (name0 (assoc-ref c0 'name))
                            (status (and (member name0 '("shell-start" "shell-read" "shell-stop"))
                                         (proc-exit-status r0)))
                            (start (and status (if (equal? name0 "shell-start")
                                                   c0
                                                   (started-call (proc-call-id c0) started-ids started-calls))))
                            (c (if start (background-call c0 start) (tool-call c0)))
                            (r (if start (background-result status) (tool-result c0 r0))))
                       (when (or (member name0 '("shell-exec" "shell-start")) (assoc name0 tool-commands))
                         (let ((cmds (call-commands (tool-call c0))))
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
                       ;; A repository tool's whole check (validate) is no
                       ;; check to take apart: it runs for minutes, and
                       ;; check-without has five for all its runs (a shell
                       ;; make validate took all five on commit-bench's
                       ;; 3422faf1, dsl18m, and named nothing). The check
                       ;; before it stays, and its pass counts as the whole
                       ;; check's (whole-check-passed?).
                       (when (and (not (base-run? c)) (or (check-run? c scripts) (test-call? c))
                                  (equal? (run-status r) 0)
                                  (not (and (assoc name0 tool-commands) (whole-check? c)))
                                  (not (validate-call? c)))
                         (set! passing c)
                         (set! passing-all (cons c passing-all)))
                       (let ((entry (run-entry c r)))
                         (when entry (set! passed (cons entry passed))))
                       (validated-run c r)
                       (when (validate-exit? c r) (set! exited (cons c0 r0)))
                       ;; The failures a test or check run reports. On the copy
                       ;; of the last commit, or in a full test run before any
                       ;; edit in a turn that began on the last commit, they
                       ;; are the unchanged code's, and stay known through the
                       ;; edits that follow; any other run's are the latest
                       ;; run's, for the failing-run reminder (failures-known?).
                       ;; A validate run is the latest run too, but its
                       ;; summary names no test: it clears last-fails, and
                       ;; teaches the loop nothing about the unchanged code.
                       (when (and (not (edit-call? c)) (or (test-call? c) (check-run? c scripts)))
                         (let* ((validating (validate-call? c))
                                (names (if validating '() (failure-names (run-output r)))))
                           (cond ((base-run? c)
                                  (set! base-fails (names-union names base-fails)))
                                 (else
                                  (let ((baseline (and (pair? names) (test-call? c) (not (check-run? c scripts)) (not (narrowed-run? c))
                                                       (not edited) (or (not before) (null? (git-tracked before)))
                                                       (not (turn-changed? before edited))))
                                        (now-outcome (run-outcome c r 'none)))
                                    (when baseline
                                      (set! base-fails (names-union names base-fails)))
                                    (cond ((equal? now-outcome 'failed)
                                           (set! last-fails names)
                                           (set! failing-call (and (not validating) (test-call? c) c))
                                           ;; The baseline run is the unchanged code's own: it
                                           ;; is not told that it failed there before.
                                           (when (and (not lean-quiet) (not baseline) (failures-known? names base-fails request written))
                                             (let ((known (car (failure-split names base-fails request written))))
                                               (unless (equal? known hinted)
                                                 (set! hinted known)
                                                 (fold-result (assoc-ref c0 'id)
                                                              (string-append (format "~a" (assoc-ref r0 'value ""))
                                                                             (failure-known-hint known)))))))
                                          ((equal? now-outcome 'passed) (set! last-fails '()) (set! failing-call #f))))))))
                       (let ((entry (shown-entry c0 c r0 r)))
                         (set! shown (shown-add shown entry))
                         (when entry (set! hand-edits (hand-edits-ran hand-edits (car entry)))))
                       (set! hand-edits (hand-edits-add hand-edits c r))
                       ;; A document's edit leaves the plan's evidence standing
                       ;; (passed): no test or check reads a .md or a .tex.
                       (when (and (edit-call? c) (assoc-ref r 'ok #f) (not (scratch-name c)) (not (doc-path? (repo-path c))))
                         (set! passed '()))
                       (when (and (edit-call? c) (assoc-ref r 'ok #f) (not (scratch-name c)) (not (doc-path? (repo-path c))))
                         (set! passing #f)
                         (set! passing-all '())
                         (set! failing-call #f)
                         (set! ran '())
                         (set! started-ids '())
                         (set! started-calls '())
                         (set! shrank (and (shrinking-edit? c sizes) #t)))
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
                                       (when (test-file-path? (repo-path c))
                                         (set! tests-edited (cons (repo-path c) tests-edited)))
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
                             ;; A run that did not pass counts toward the way out
                             ;; (finish-unrunnable-message) only if it shows no
                             ;; sign that tests run here: failing tests ran, and a
                             ;; run after an edit that took part of the change out
                             ;; is a revert experiment, expected to fail (855e90e7:
                             ;; six in a row were told "did not run successfully").
                             ((test-call? c)
                              (walk (cdr cs) (cdr rs) edited #t
                                    (cond ((or (ran-ok? r) (tests-ran? r)) 0)
                                          (shrank failed)
                                          (else (+ failed 1)))
                                    scripts written (run-outcome c r outcome) told))
                             (else (walk (cdr cs) (cdr rs) edited tested failed scripts written outcome told)))))
                    (else
                     ;; What the round's validate runs and tools left untracked
                     ;; is said in the validate result, and kept for
                     ;; validate-settle.
                     (let ((made (if (or exited (tool-round? calls)) (files-made tool-before (git-state) written) '())))
                       (set! validate-made (names-union made validate-made))
                       (when exited
                         (when (pair? made)
                           (fold-result (assoc-ref (car exited) 'id)
                                        (string-append (format "~a" (assoc-ref (cdr exited) 'value "")) (validate-left-note made))))
                         (set! validate-print (repo-print))
                         (set! exited #f))
                       (set! validate-print (validate-settle validate-made validate-print)))
                     (maybe-compact request reply)
                     (let ((stuck (and (not offered) (>= failed unrunnable-after)))
                           (reading (reading-rounds calls reading))
                           (outcome (keep-passed outcome before edited)))
                       (when stuck (add-message (finish-unrunnable-message failed)))
                       ;; What the reply says to the question asked at the end
                       ;; of an earlier round is read before a new one is asked.
                       (set! edit-asked (car (edit-answers request (format "~a" (assoc-ref reply 'content "")) edit-asked #f)))
                       (let ((asked (test-edit-nudge request tests-edited tests-told)))
                         (set! tests-told (append (car asked) tests-told))
                         (set! edit-asked (append edit-asked (map (lambda (l) (cons 'asked l)) (cdr asked)))))
                       (set! tests-edited '())
                       (loop (ask-model) edited tested failed (or offered stuck) reminders scripts written excused
                             reading outcome told unchanged-told)))))
            (let* (;; The answer that ends the turn is read too, for the rows it
                   ;; supports; the rows it does not are the finish review's.
                   (read (begin (set! edit-asked (car (edit-answers request (format "~a" (assoc-ref reply 'content "")) edit-asked #t))) #t))
                   (now (and before (git-state)))
                   (outcome (if (and (equal? outcome 'failed) (failures-known? last-fails base-fails request written))
                                'compared
                                outcome))
                   (failing-msg (and (equal? outcome 'failed) (not offered) (not told)
                                     (failing-reminder last-fails base-fails request written)))
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
                   (gap (cond ((or turn-stopping review-made) #f)
                              ;; In sandbox mode (ADR 54) the whole machine is the
                              ;; workspace: neither git nor the file tools see what a
                              ;; command changed outside the root (a config in /etc, a
                              ;; package), so nothing says that no file changed.
                              ((and (not changed) (not excused) (not (and unchanged-told tested)) (not (sandbox?)))
                               (cond (lean-quiet finish-no-change-lean-message)
                                     ((reviewed?) finish-no-change-reviewed-message)
                                     (else finish-no-change-message)))
                              ((and changed (not tested) (not offered)) finish-untested-message)
                              ((and changed failing-msg) failing-msg)
                              (else #f))))
              (cond ((and gap (< reminders (if (member gap (list finish-no-change-message finish-no-change-reviewed-message finish-no-change-lean-message))
                                                 (max finish-reminders lean-empty-reminders)
                                                 finish-reminders)))
                     (add-message gap)
                     ;; A run the model was told failed is not told again
                     ;; until an edit: it may answer that the tests fail on the
                     ;; unchanged code too, and end.
                     (loop (ask-model) edited tested failed-runs offered (+ reminders 1) scripts written
                           (or excused (and (not changed) (reviewed?))) 0 outcome
                           (or told (and failing-msg (equal? gap failing-msg)))
                           (or unchanged-told (not changed))))
                    ;; The finish review, once a turn: what lacks evidence or
                    ;; may be wrong, said in one message. An answer after it
                    ;; ends the turn (the gap above is not asked again): on
                    ;; commit-bench the checks chained, each answer an edit or
                    ;; a script that re-armed the next: after the first report,
                    ;; 0.41-0.85M tokens over five tasks, in segments 17 of 21
                    ;; of which edited nothing (ADR 32, amended 2026-10-15).
                    ((and changed (not turn-stopping) (not review-made) (finish-review-due?))
                     (set! review-made #t)
                     (let ((points (finish-review-points
                                    request reply
                                    (if now
                                        (filter (lambda (f) (not (plan-names? f))) (git-new-files before now))
                                        '())
                                    (and (memq outcome '(passed kept))
                                         (broadest-passing passing-all (append written (map git-state-path new-lines))))
                                    (memq outcome '(passed kept))
                                    (append ran base-ran) ran-all
                                    (or (and passing (whole-check? passing)) (whole-check-passed? passed))
                                    passed shown hand-edits validate-made
                                    (append written (map git-state-path new-lines))
                                    edit-asked passing-all (and (equal? outcome 'failed) failing-call))))
                       (and (pair? points)
                            (begin (add-message (finish-review-message points))
                                   (finish-review-counted!)
                                   (loop (ask-model) edited tested failed-runs offered reminders scripts written excused 0
                                         outcome told unchanged-told)))))
                    (else #f))))))))
