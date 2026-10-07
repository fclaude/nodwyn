;; The plan gate (SPEC "plan trigger", ADR 19 Seam A), as user code. Before a
;; turn's work the judge says whether the request needs a plan; if it does,
;; the control model drafts one in a context of its own, the judge scores it
;; against the request, and the model revises it at most plan-max-revisions
;; times. The outcome is recorded (record-plan), and the window shows the plan
;; to the model for the rest of the turn.
;;
;; Without a judge there is no gate, unless the turn must plan: a run's turns
;; do ((plan-required?)). Then the model always drafts a plan, scored if a
;; judge is there, and the core refuses anything irreversible until a plan is
;; recorded.
;;
;; A run's plan is the run's, not the turn's: a later turn carries the plan
;; an earlier one recorded, with the steps marked so far (plan-carry), and
;; works on its next unmarked step. Only once every step is marked, and the
;; run goes on, does a turn plan again, told how the last plan went. A model
;; that finds the plan wrong marks the steps that no longer apply skipped.
;;
;; A chat's turn that did not finish (an error, a limit or the person's Stop
;; ended it) leaves its plan to the next message, "continue" or a
;; correction, which carries it the same way (unfinished-plan). Without
;; that the plan went with the turn: plan-step refused to mark its steps,
;; and the model read the repository again and wrote the plan again, its
;; done steps included. A turn that finished, its answer given, leaves none:
;; the next message is a new request.
;;
;; A project may redefine any of these, or plan-gate itself.

(define plan-needed-threshold 0.5) ; the judge's "needs a plan" probability at which a request gets one
(define plan-accept-score 0.7)     ; the normalised score at which a plan is accepted
(define plan-max-revisions 3)      ; revisions after the first draft (SPEC: at most 3)
(define plan-empty-retries 2)      ; more times the planner is asked when it gives no plan
;; The explorer's tool rounds before a plan is drafted; 0 skips it. A table,
;; by family (tuned, strategy.scm): a round is one reply's calls, and models
;; batch them very differently. In the local session logs (16 explorer runs,
;; 2026-10-05) gpt-6.1-sol made 5 to 6.6 calls a round and finished in 7 to
;; 10, claude-sonnet-5-5 made 2.5 to 3.3 and finished in 4 and 10, but
;; gpt-6-luna (2 of 2) and muse-spark (5 of 7), at 3.4 to 5, were stopped at
;; the twelfth with calls still asked for and wrote their report from what
;; they had. glm, qwen and deepseek have no explorer run there; they batch as
;; muse-spark does in the main loop (1.2 to 1.4 calls a round), so they share
;; its row. 16 is a first step, to be read against the next logs.
(define plan-explore-rounds
  '(("muse-spark" . 16) ("glm" . 16) ("qwen" . 16) ("deepseek" . 16) ("gpt-6-luna" . 16) (default . 12)))
(define plan-earlier-requests 2)   ; a chat's earlier messages the gate reads with its latest
(define plan-earlier-cap 2000)     ; the characters of each it reads

;; A chat's latest request as the gate reads it: after the person's last
;; plan-earlier-requests messages before it, each cut at plan-earlier-cap
;; characters; alone in a chat's first turn. A reply to the model's
;; question means little alone: in session 20261005T060224Z-2f9eb922,
;; "folding, to reduce visual polution for the user" said which of two
;; things "that is still verbose, compacting should be possible there" had
;; meant, and the judge, shown only the reply, put the need for a plan at
;; 0.4 for a change to the transcript's rendering, its styles, its browser
;; checks and four documents. The explorer and the planner read the same
;; text, since a plan for the reply alone would be written blind.
(define (plan-chat-request request)
  (let* ((asked (session-requests))
         (earlier (if (pair? asked) (reverse (cdr (reverse asked))) '()))
         (recent (list-tail earlier (max 0 (- (length earlier) plan-earlier-requests)))))
    (if (null? recent)
        request
        (plan-request-with-earlier (map plan-cut recent) request))))

;; A text cut at plan-earlier-cap characters.
(define (plan-cut r)
  (if (> (string-length r) plan-earlier-cap)
      (string-append (substring r 0 plan-earlier-cap) "…")
      r))

;; What the explorer and the planner read: the request as the judge reads
;; it (asked, from plan-chat-request), after the agent's last answer, which
;; a reply answers, and with the names of the files attached to it, which a
;; context of their own does not show. In session 20261005T060224Z-2f9eb922
;; the explorer, shown neither, said the request "does not identify what
;; 'there' refers to" when a screenshot and the agent's own question did;
;; in 20261005T151325Z-d9519026 the explorer and the planner of "fix all of
;; them" could not see the findings the agent's last answer listed. The
;; judge is not shown them: at judge.content "plan" it reads the person's
;; asks and the plan, and the agent's replies are not sent (ADR 19).
(define (plan-model-request asked)
  (let* ((context (guard (e (else '())) (request-context)))
         (reply (plan-cut (format "~a" (assoc-ref context 'reply ""))))
         (files (assoc-ref context 'attachments '())))
    (string-append
     (if (string-empty? (string-trim reply)) "" (plan-request-after-reply reply))
     asked
     (if (pair? files) (plan-request-attachments files) ""))))

;; What a read-only look at the repository finds for request, from a
;; sub-agent labelled "explorer" (agents.scm), so the plan is not written
;; blind; "" when exploring is off or finds nothing. Its reads happen before
;; any plan is recorded, which the core allows: none is irreversible.
(define (plan-explore request)
  (if (<= (tuned plan-explore-rounds) 0)
      ""
      (let ((rounds sub-agent-max-rounds))
        (set! sub-agent-max-rounds (tuned plan-explore-rounds))
        (let ((found (guard (e (else (set! sub-agent-max-rounds rounds) ""))
                       (sub-agent plan-explore-system (plan-explore-task request)
                                  '("fs-read" "fs-list" "index-grep" "fs-find") "explorer"))))
          (set! sub-agent-max-rounds rounds)
          found))))

;; Whether a judge is configured: [judge] on a provider whose key was stored
;; when the session started, which is when the core registers the judge's
;; primitives (ADR 46).
(define (plan-judge?)
  (guard (e (else #f)) (primitive-info "judge-yes-no") #t))

;; A plan from the control model, asked in a context of its own that is then
;; folded away: the conversation keeps only the plan the gate records. The
;; planner is offered no tools, so it answers in text. A model sometimes
;; answers with thinking only; it is then asked again, in a fresh context, up
;; to plan-empty-retries more times, each noted in the log.
(define (plan-ask prompt)
  (let ask ((attempt 0))
    (narrow-context (plan-system-prompt (repository)) "planner")
    (add-message prompt)
    (let* ((reply (ask-model "control" '((tools . #f))))
           (text (assoc-ref reply 'content "")))
      (skip-tools (assoc-ref reply 'tool-calls '())
                  "Plan only: reply with the plan as text; the work comes after it.")
      (fold-context)
      (if (and (string-empty? (string-trim text)) (< attempt plan-empty-retries))
          (begin
            (log-note (string-append "planner returned an empty plan, asking again ("
                                     (number->string (+ attempt 1)) "/"
                                     (number->string plan-empty-retries) ")"))
            (ask (+ attempt 1)))
          text))))

;; The judge's normalised score (0 to 1) for plan against request, or the
;; error it failed with.
(define (plan-score request plan)
  (guard (e (else e))
    (assoc-ref (judge-score (string-append "Request:\n" request "\n\nPlan:\n" plan)
                            plan-score-rubric plan-score-levels)
               "score")))

;; Drafts a plan and, when judged, revises it until it scores
;; plan-accept-score or the revisions run out. A judge failure, or a
;; revision that comes back empty, keeps the best plan so far. Records the
;; best plan under decision, and returns it.
;; The judge scores against request; the explorer and the planner read
;; seen, when given, instead (plan-model-request).
(define (plan-best judged request decision . seen)
  (define shown (if (pair? seen) (car seen) request))
  (define findings (plan-explore shown))
  (define (keep best best-score revisions reason)
    (record-plan (list (cons 'decision decision) (cons 'plan best) (cons 'score best-score)
                       (cons 'revisions revisions) (cons 'reason reason)))
    (plan-hand-over findings)
    best)
  (let loop ((plan (plan-ask (plan-draft-with-findings shown findings)))
             (round 0) (best #f) (best-score 0) (revisions 0))
    (let* ((score (if judged (plan-score request plan) #f))
           (scored (number? score))
           (better (or (not best) (and scored (> score best-score))))
           (best (if better plan best))
           (best-score (if (and better scored) score best-score)))
      (cond ((and judged (not scored))
             (keep best best-score revisions
                   (string-append "judge-score failed at round " (number->string round)
                                  ": " (assoc-ref score 'message ""))))
            ((or (not scored) (>= score plan-accept-score) (= round plan-max-revisions))
             (keep best best-score revisions ""))
            (else
             (let ((revised (plan-ask (plan-revise-prompt shown plan score))))
               (if (string-empty? (string-trim revised))
                   (keep best best-score revisions "")
                   (loop revised (+ round 1) best best-score (+ revisions 1)))))))))

;; What exploring found goes to the model that does the work, not only to
;; the planner: it would otherwise read the same files again to learn it.
(define (plan-hand-over findings)
  (unless (string-empty? (string-trim findings))
    (add-message (plan-findings-message findings))))

;; A run's later turn, or the turn after one that did not finish: the plan
;; again, with the marks the earlier turns made, so the steps keep their
;; numbers and the window shows what is done.
(define (plan-carry run)
  (record-plan (list (cons 'decision "carried") (cons 'plan (assoc-ref run 'plan))
                     (cons 'marks (assoc-ref run 'marks '())))))

;; The plan a turn that did not finish left with a step unmarked, or #f.
(define (plan-left)
  (let ((left (unfinished-plan)))
    (and left (assoc-ref left 'next #f) left)))

(define (plan-gate request)
  (let ((judged (plan-judge?)))
    (cond ((plan-recorded?) #f) ; this turn has its plan already
          ((plan-required?)
           (let ((run (run-plan)))
             (if (and run (assoc-ref run 'next #f))
                 (plan-carry run)
                 (let ((plan (plan-best judged (plan-run-request request run) (if judged "plan" "required"))))
                   (when (string-empty? (string-trim plan))
                     (error "required plan: the planner returned an empty plan"))))))
          ((plan-left) => plan-carry)
          ((not judged) #f)
          (else
           (let* ((asked (plan-chat-request request))
                  (seen (plan-model-request asked))
                  (p (guard (e (else e)) (judge-yes-no asked plan-needed-question))))
             (cond ((not (number? p)) ; the judge failed: plan anyway, unscored
                    (record-plan (list (cons 'decision "degraded")
                                       (cons 'plan (plan-ask (plan-draft-prompt seen)))
                                       (cons 'reason (string-append "judge-yes-no failed: " (assoc-ref p 'message ""))))))
                   ((< p plan-needed-threshold)
                    (record-plan (list (cons 'decision "proceed") (cons 'score p))))
                   (else (plan-best judged asked "plan" seen))))))))

;; Plan step progress. The model marks a step of the turn's plan done, or
;; skipped, as it goes (plan-step); the strategy records the mark
;; (record-step, as plan.step) and the console shows the plan's progress. A
;; mark is the model's word, like its report: the evidence is the transcript.
(define plan-step-no-plan "No plan was recorded in this turn, so there is no step to mark: write one with plan-write first.")

(define (plan-step-answer call args)
  (let ((steps (plan-steps))
        (n (assoc-ref args "step" #f))
        (state (assoc-ref args "state" "done"))
        (note (format "~a" (assoc-ref args "note" ""))))
    (cond ((null? steps) plan-step-no-plan)
          ((not (and (integer? n) (>= n 1) (<= n (length steps))))
           (string-append "There is no step " (format "~a" n) ": the plan has "
                          (number->string (length steps)) " steps, numbered from 1."))
          ((not (member state '("done" "skipped")))
           "A step is marked done or skipped.")
          (else
           (let* ((progress (record-step n state note))
                  (next (assoc-ref progress 'next #f)))
             (string-append "Step " (number->string n) " is " state ". "
                            (if next
                                (string-append "Next unmarked: " (number->string next) ". " (list-ref steps (- next 1)))
                                "Every step of the plan is marked.")))))))

(define-agent-tool "plan-step"
  "Mark a step of the current plan done as soon as its work is done, before you start the next step — never several together at the end: whoever follows the task sees the plan's progress move as the work happens. Mark it skipped when you decided against it, with what shows it: a test that passed, the file you changed, or why you skipped it. A Test: step is done once its test has failed without your change and passes with it (or passed both times, for one that guards behaviour that already works): the note names both runs. Steps are numbered from 1, as in the plan."
  '(("step" "integer" "the step's number in the plan")
    ("state" "string" "done or skipped")
    ("note" "string" "what shows it"))
  (lambda (call args)
    (guard (e (else (string-append "plan-step failed: " (format "~a" (assoc-ref e 'message "")))))
      (plan-step-answer call args))))

;; The shape plan-write asks of a plan, and the gate's planner and judge
;; (prompts.scm) ask for too (ADR 32, amended 2026-10-03). The worktree
;; sessions of 2026-10-03 planned in stage labels ("2. WT0: repository
;; lookup ..., with tests"): no step said which test or which doc, so an
;; ADR's clauses never became steps, a test passed on the code it was
;; written to catch, a browser check only found the new checkbox, and a
;; new primitive was missing from the book's tables. Each step starts with
;; its kind, the word before its first ":" in any case; plan-kinds says
;; what a step of each kind names, and the messages below say it the same
;; way. Each step also gives a short name for what it does before the
;; detail, which the console folds away (transcript.js stepTitle): the
;; plans of 2026-10-03 the person screenshotted read "Change: read
;; internal/frontend/rpc/workspaces.go, the desk state builder and …", the
;; whole work as the title, cut at the console's cap, with nothing to fold
;; to. plan-name-cap says how long a name may be, over the boundaries the
;; console folds on. A project may redefine any of these, or plan-problems.
(define plan-kinds
  '(("Change" . "a file (or function) and what changes in it")
    ("Test" . "the test file and name, the behaviour it checks, and why it fails on the unchanged code (\"fails today: …\"), or \"passes today: guards …\" for a test of behaviour that already works")
    ("Docs" . "each file a person or a later session reads that the change makes wrong or incomplete")
    ("Check" . "the command that shows the whole change works")))
(define plan-required-kinds '("Change" "Test" "Docs")) ; a plan has a step of each
(define plan-last-kind "Check")                         ; and ends with a step of this one; #f for any
(define plan-none-kinds '("Test" "Docs"))               ; whose step may say "none" and why instead
;; What a Test: step says of the unchanged code, in any case: how its test
;; fails there, or that it passes there and guards what already works.
(define plan-test-reasons '("fails today" "passes today" "fails on the unchanged code" "passes on the unchanged code"))
(define plan-write-refusals 2) ; plans turned down in a turn before plan-write records one as written

;; Text as the checks compare it: lower case, trimmed, without the Markdown
;; emphasis a model may put around a kind ("**Test:**").
(define (plan-plain text)
  (string-downcase (string-trim (string-replace (string-replace text "*" "") "`" ""))))

;; A step's kind as plan-kinds spells it, or #f.
(define (plan-step-kind step)
  (let ((parts (string-split step ":")))
    (and (pair? (cdr parts))
         (let ((word (plan-plain (car parts))))
           (let find ((kinds plan-kinds))
             (cond ((null? kinds) #f)
                   ((equal? (string-downcase (caar kinds)) word) (caar kinds))
                   (else (find (cdr kinds)))))))))

;; What a step says after its kind, as plan-plain gives it.
(define (plan-step-said step)
  (plan-plain (string-join (cdr (string-split step ":")) ":")))

;; Whether a Test: step says how its test fares on the unchanged code, or
;; that there is no test.
(define (plan-test-explained? step)
  (let ((said (plan-step-said step)))
    (or (and (member "Test" plan-none-kinds) (string-prefix? "none" said))
        (pair? (filter (lambda (r) (string-contains? said (string-downcase r))) plan-test-reasons)))))

;; A step's short name ends at the first boundary the console folds a step
;; on (transcript.js stepTitle): a dash with a space before it, or a
;; sentence end or semicolon with a space after. A step with no boundary is
;; its own name, so it must be short to show whole.
(define plan-name-cap 80)
(define plan-name-boundaries '(" —" " –" " --" ". " "; "))

;; The index of a step's first name boundary, or #f when there is none.
(define (plan-name-at step)
  (let scan ((i 0) (n (string-length step)))
    (cond ((>= i n) #f)
          ((let find ((bs plan-name-boundaries))
             (and (pair? bs)
                  (or (string-prefix? (car bs) (substring step i n))
                      (find (cdr bs)))))
           i)
          (else (scan (+ i 1) n)))))

;; A step's short name in characters: everything before its first boundary,
;; or the whole step when there is none.
(define (plan-name-length step)
  (or (plan-name-at step) (string-length step)))

;; "Change:, Test:, Docs: or Check:", from plan-kinds.
(define (plan-kind-names)
  (let ((names (map (lambda (k) (string-append (car k) ":")) plan-kinds)))
    (if (null? (cdr names))
        (car names)
        (string-append (string-join (reverse (cdr (reverse names))) ", ") " or " (car (reverse names))))))

;; What is wrong with a plan's steps, one message each: step by step
;; first, then the plan as a whole. '() when nothing is.
(define (plan-problems steps)
  (let ((kinds (map plan-step-kind steps))
        (n (length steps)))
    (append
     (apply append
            (map (lambda (i step kind)
                   (cond ((not kind) (list (plan-kind-problem (+ i 1))))
                         ((and (equal? kind "Test") (not (plan-test-explained? step)))
                          (list (plan-test-problem (+ i 1))))
                         ((> (plan-name-length step) plan-name-cap)
                          (list (plan-name-problem (+ i 1))))
                         (else '())))
                 (iota n) steps kinds))
     (map plan-missing-problem (filter (lambda (k) (not (member k kinds))) plan-required-kinds))
     (if (and plan-last-kind (> n 0) (not (equal? (list-ref kinds (- n 1)) plan-last-kind)))
         (list (plan-last-problem n))
         '()))))

;; The messages plan-write answers with.
(define (plan-kind-description kind)
  (let ((k (assoc kind plan-kinds))) (if k (cdr k) "")))
(define (plan-kind-problem n)
  (string-append "Step " (number->string n) " has no kind: start it with " (plan-kind-names)))
(define (plan-test-problem n)
  (string-append "Step " (number->string n) " is a Test: step that does not say why its test fails on the unchanged code: add \"fails today: …\", or \"passes today: guards …\" for a test of behaviour that already works"))
(define (plan-name-problem n)
  (string-append "Step " (number->string n) " has no short name: name what it does within " (number->string plan-name-cap)
                 " characters, then a dash (—) and the detail after it"))
(define (plan-missing-problem kind)
  (string-append "There is no " kind ": step: add one naming " (plan-kind-description kind)
                 (if (member kind plan-none-kinds)
                     (string-append ", or one saying why there is none (\"" kind ": none — why\")")
                     "")))
(define (plan-last-problem n)
  (string-append "The last step, " (number->string n) ", is not a " plan-last-kind ": step: end the plan with one naming "
                 (plan-kind-description plan-last-kind)))
(define (plan-problem-lines problems)
  (apply string-append (map (lambda (p) (string-append "\n- " p ".")) problems)))
(define (plan-refused-message problems)
  (string-append "Plan not recorded: each step starts with its kind, and the plan names its tests and docs. Fix this and write it again with plan-write:"
                 (plan-problem-lines problems)))
(define (plan-recorded-message n problems)
  (string-append "Plan recorded: " (number->string n) (if (= n 1) " step" " steps")
                 (if (null? problems)
                     ". It stays in front of you; mark each step with plan-step as its work is done, before you start the next — never several together at the end."
                     (string-append ", as written, since plan-write has turned down " (number->string plan-write-refusals)
                                    " plans in this turn. Still missing:" (plan-problem-lines problems)
                                    "\nIt stays in front of you: cover what is missing as you work, and mark each step with plan-step as its work is done, before you start the next — never several together at the end."))))

;; plan-write's refusals in a turn: the turn, as the number of the person's
;; messages so far (read from the log, so a project that redefines
;; nodwyn-turn does not keep the count from starting over), and how many.
(define plan-write-refused (cons 0 0))

;; The model's own plan. Its thinking was not sent back, and a reply that
;; only calls tools keeps nothing either, so a design worked out in
;; thinking was worked out again at every request (a chat turn re-derived
;; one 30 times and changed nothing). It is sent back now (window recipe
;; v14), but only as the server reported it, which may be a summary or
;; nothing, and the window may trim it. plan-write records the
;; model's plan, decision "model", which the window shows under
;; plan-header once the call is answered, and plan-step marks it. Writing it
;; again revises it, and its marks start over; in a run it becomes the
;; run's plan, and a turn that does not finish leaves it to the next.
;;
;; A plan without the shape above is turned down, unrecorded, with what it
;; lacks. After plan-write-refusals of those in a turn the next plan is
;; recorded as written, with what it still lacks, so a model that cannot
;; get the shape right is not kept from the work; so is any plan after it
;; in that turn. The console hides a reply that starts "Plan recorded: "
;; (transcript.js), since its plan panel shows the plan.
(define (plan-write-answer call args)
  (let ((plan (string-trim (format "~a" (assoc-ref args "plan" "")))))
    (if (string-empty? plan)
        "A plan needs at least one step."
        (let* ((problems (plan-problems (plan-steps plan)))
               (turn (length (session-requests)))
               (refused (if (= (car plan-write-refused) turn) (cdr plan-write-refused) 0)))
          (if (and (pair? problems) (< refused plan-write-refusals))
              (begin
                (set! plan-write-refused (cons turn (+ refused 1)))
                (plan-refused-message problems))
              (begin
                (record-plan (list (cons 'decision "model") (cons 'plan plan)))
                (plan-recorded-message (length (plan-steps)) problems)))))))

(define-agent-tool "plan-write"
  "Write down your plan for the task: numbered steps, each starting with its kind and a short name for what it does, then a dash (—) and the detail after it. Change: a file (or function) and what changes in it. Test: the test file and name, the behaviour it checks, and why it fails on the unchanged code (\"fails today: …\"), or \"passes today: guards …\" for a test of behaviour that already works. Docs: each file a person or a later session reads that the change makes wrong or incomplete. Check: the command that shows the whole change works, as the last step. With no test or no doc to change, say so and why (\"Test: none — only docs change\"). For example:\n1. Change: src/flags.go parseFlags — accept --verbose.\n2. Test: src/flags_test.go TestVerboseFlag — --verbose sets Verbose; fails today: the flag is unknown.\nYour thinking comes back to you only as the server reported it, often as a summary or not at all; the plan stays in front of you for the rest of the task, and plan-step marks its steps as the work happens. Write it once you know what to change, before changing it, and write it again only when it must change."
  '(("plan" "string" "the numbered steps, each starting with Change:, Test:, Docs: or Check:, a short name for what it does, then — and the detail; the last a Check:"))
  (lambda (call args)
    (guard (e (else (string-append "plan-write failed: " (format "~a" (assoc-ref e 'message "")))))
      (plan-write-answer call args))))
