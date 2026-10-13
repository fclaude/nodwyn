;; A plan as data (ADR 32, amended 2026-10-13). The planner writes its plan
;; as Scheme forms, one per step, instead of sentences:
;;
;;   (change "src/flags.go" "parseFlags" "accept --verbose and set Verbose")
;;   (test "src/flags_test.go" "TestVerboseFlag" "--verbose sets Verbose"
;;         (fails-today "the flag is unknown") (run "go test ./src -run TestVerboseFlag"))
;;   (docs "README.md" "the flags section lists --verbose")
;;   (names "--verbose" "Verbose")
;;   (check "go test ./...")
;;
;; so what a plan says is a set of things the harness can look for: the
;; files a step changes, the test it adds and the command that runs it, the
;; names the change must produce. The plan the model reads is the same steps
;; written out as before (plan-language-text), so the window, the console and
;; the marks are unchanged.
;;
;; Three things follow from the data. Every name the request gives must be
;; in the plan, as something the change produces (names) or only uses
;; (context): on commit-bench (556d6d12, 2026-10-12) the request named three
;; headers and two planners read session-id as the X-Session-Id the code
;; already sent; one dropped it, and the change failed. Before the turn
;; ends, each step is set against what happened (plan-language-unmet): the
;; file changed, the named test is in its file and passed after the last
;; edit, the check passed after it, and the change contains every name the
;; plan says it produces; what is missing is said once. And the steps that
;; have their evidence are marked by the harness, so the model spends no
;; request on marking them: 69 of 103 replies that marked a step held nothing
;; else on the same benchmark.
;;
;; A reply in prose is kept as the prose plan it is, and nothing here
;; applies to it; one that starts as forms but does not read, or reads with
;; problems, is asked for again, at most plan-language-retries times.

(define plan-language #t)        ; #f: plans are prose, as before
(define plan-language-retries 2) ; replies the planner may give that are not usable plans before its prose is kept

;; The current turn's plan as data: '() when it has none. Each step is an
;; alist: kind ("Change", "Test", "Docs" or "Check"), path, name (the
;; function or the test), what, today ("fails today: ..." or "passes today:
;; ..."), run (the command), none (#t for a "none" step); and the plan's
;; names and context.
(define plan-spec '())
(define plan-spec-names '())
(define plan-spec-context '())

;; The request the planner plans for, for its names (plan-language-names).
(define plan-language-request "")

(define plan-language-base-turn nodwyn-turn)
(define (nodwyn-turn request)
  (set! plan-spec '())
  (set! plan-spec-names '())
  (set! plan-spec-context '())
  (set! plan-language-marked '())
  (set! plan-language-edited '())
  (set! plan-language-before #f)
  (set! plan-language-as-before '())
  (set! plan-language-before-noted 0)
  (set! plan-language-tests-before '())
  (set! plan-language-started-ids '())
  (set! plan-language-started-lines '())
  (set! plan-language-started-texts '())
  (set! plan-language-missing-told '())
  (set! plan-language-auto-passed '())
  (set! plan-language-auto-runs 0)
  (plan-language-base-turn request))

;; The steps the harness has marked in this turn, and the repository paths
;; the turn's edits wrote (plan-language-round).
(define plan-language-marked '())
(define plan-language-edited '())

;; A step marked done, once.
(define (plan-language-mark-one n note)
  (unless (member n plan-language-marked)
    (set! plan-language-marked (cons n plan-language-marked))
    (guard (e (else #f)) (record-step n "done" note))))

(define (plan-language-same-path? a b)
  (let* ((strip (lambda (p) (if (string-prefix? "./" p) (substring p 2 (string-length p)) p)))
         (a (strip a)) (b (strip b))
         (dir (if (string-suffix? "/" a) a (string-append a "/"))))
    ;; A step may name a directory (docs/adr/): an edit to a file in it is
    ;; its change.
    (or (equal? a b) (and (not (string-empty? a)) (string-prefix? dir b)))))

;; Marks as the work happens: after each round of the main context's calls,
;; a change or docs step whose file a call wrote, a test step whose file was
;; written and whose test a command that passed named, and a check step whose
;; command passed. The marks are the console's progress; what counts at the
;; end is plan-language-check, after the last edit.
(define (plan-language-round calls results)
  (let walk ((cs calls) (rs results))
    (when (and (pair? cs) (pair? rs))
      (let* ((c (car cs)) (r (car rs)) (name (assoc-ref c 'name ""))
             (a (guard (e (else '())) (json-parse (format "~a" (assoc-ref c 'args "{}"))))))
        (cond ((and (member name '("fs-edit" "fs-write")) (assoc-ref r 'ok #f) (pair? a))
               (let ((path (format "~a" (assoc-ref a "path" ""))))
                 (set! plan-language-edited (cons path plan-language-edited))
                 (let mark ((ss plan-spec) (i 1))
                   (when (pair? ss)
                     (when (and (member (assoc-ref (car ss) 'kind "") '("Change" "Docs"))
                                (plan-language-same-path? (assoc-ref (car ss) 'path "") path))
                       (plan-language-mark-one i (string-append "evidence: " path " written")))
                     (mark (cdr ss) (+ i 1))))))
              ((and (equal? name "shell-exec") (equal? (run-status r) 0))
               (let ((line (command-line c)) (notes '()))
                 (let mark ((ss plan-spec) (i 1))
                   (when (pair? ss)
                     (let* ((st (car ss)) (kind (assoc-ref st 'kind "")) (run (assoc-ref st 'run "")))
                       (cond ((and (equal? kind "Test")
                                   (let any ((ps plan-language-edited)) (and (pair? ps) (or (plan-language-same-path? (car ps) (assoc-ref st 'path "")) (any (cdr ps)))))
                                   (or (string-contains? line (assoc-ref st 'name "_"))
                                       (and (not (string-empty? run)) (string-contains? line run))))
                              (let ((new (not (member i plan-language-marked))))
                                (plan-language-mark-one i (string-append "evidence: " (assoc-ref st 'name "") " passed"))
                                ;; Its first pass: is it a test of the change?
                                (when new
                                  (let ((note (plan-language-test-note st)))
                                    (when note (set! notes (cons note notes)))))))
                             ((and (equal? kind "Check") (not (string-empty? run)) (string-contains? line run))
                              (plan-language-mark-one i "evidence: the check passed"))))
                     (mark (cdr ss) (+ i 1))))
                 (when (pair? notes)
                   (fold-result (assoc-ref c 'id)
                                (string-append (format "~a" (assoc-ref r 'value "")) (apply string-append (reverse notes))))))))
        (walk (cdr cs) (cdr rs))))))

(define plan-language-base-run-calls run-calls)
(define (run-calls calls)
  (let ((results (plan-language-base-run-calls calls)))
    (when (and plan-language (= (guard (e (else 0)) hygiene-depth) 0))
      (guard (e (else (log-note (format "plan language: new files were not marked: ~a" e))))
        (plan-language-intend calls results)))
    (when (and plan-language (pair? plan-spec) (= (guard (e (else 0)) hygiene-depth) 0))
      (guard (e (else (log-note (format "plan language: the round's commands were not read: ~a" e))))
        (plan-language-before-round calls results))
      (guard (e (else (log-note (format "plan language: the round's marks were not made: ~a" e))))
        (plan-language-round calls results))
      (guard (e (else (log-note (format "plan language: the plan's tests did not run: ~a" e))))
        (plan-language-test-round calls results)))
    results))

;; The plan's tests, run by the harness after each round that edited the
;; code (2026-10-13, dsl5): the model writes the test and the code, and the
;; result of the round's last edit says how each planned test now runs, so
;; the model need not ask for a run to see it. In dsl3's attempts a third of
;; the main loop's calls ran tests or checks (556d6d12: 24 of 66).
;; A test runs when its file holds its name and its command names it (a
;; command that runs one test, not a whole suite).
(define plan-language-auto-passed '())   ; the tests the harness saw pass since the last edit
(define plan-language-auto-runs 0)       ; harness runs of tests in the turn

(define (plan-language-runnable-tests)
  (filter (lambda (st)
            (let ((name (assoc-ref st 'name "")) (run (assoc-ref st 'run "")))
              (and (equal? (assoc-ref st 'kind "") "Test") (not (assoc-ref st 'none #f))
                   (not (string-empty? name)) (string-contains? run name)
                   (string-contains? (guard (e (else "")) (format "~a" (fs-read (assoc-ref st 'path "")))) name))))
          plan-spec))

(define (plan-language-step-number st)
  (let find ((ss plan-spec) (i 1))
    (cond ((null? ss) #f) ((eq? (car ss) st) i) (else (find (cdr ss) (+ i 1))))))

(define (plan-language-test-round calls results)
  (let find ((cs calls) (rs results) (last #f))
    (cond ((pair? cs)
           (find (cdr cs) (cdr rs) (if (plan-language-code-edit? (car cs) (car rs)) (cons (car cs) (car rs)) last)))
          (last
           (set! plan-language-auto-passed '())
           (let ((tests (plan-language-runnable-tests)))
             (when (pair? tests)
               (let ((lines
                      (map (lambda (st)
                             (let* ((name (assoc-ref st 'name "")) (run (assoc-ref st 'run ""))
                                    (r (guard (e (else #f)) (plan-run "bash" (list "-c" run))))
                                    (status (and (pair? r) (assoc-ref r "status" #f))))
                               (set! plan-language-auto-runs (+ plan-language-auto-runs 1))
                               (cond ((not (integer? status)) (string-append name ": could not run"))
                                     ((= status 0)
                                      (set! plan-language-auto-passed (cons name plan-language-auto-passed))
                                      (let* ((i (plan-language-step-number st))
                                             (new (and i (not (member i plan-language-marked)))))
                                        (when i (plan-language-mark-one i (string-append "evidence: " name " passed (run by the harness)")))
                                        (string-append name ": passes"
                                                       (let ((note (and new (plan-language-test-note st))))
                                                         (if note (string-append " —" (substring note 7 (- (string-length note) 1))) "")))))
                                     (else
                                      (string-append name ": fails (exit " (number->string status) ")\n"
                                                     (plan-language-last-lines (format "~a" (assoc-ref r "tail" "")) 15))))))
                           tests)))
                 (let ((c (car last)) (r (cdr last)))
                   (fold-result (assoc-ref c 'id)
                                (string-append (format "~a" (assoc-ref r 'value ""))
                                               "\n[hint: the plan's tests after this edit, run by the harness:\n"
                                               (string-join lines "\n") "]")))))))
          (else #f))))

;; The last n lines of a text.
(define (plan-language-last-lines text n)
  (let* ((ls (string-split text "\n")) (k (length ls)))
    (string-join (if (> k n) (list-tail ls (- k n)) ls) "\n")))

;; Whether the harness's own runs since the last edit count for an
;; unattended turn's "you have not tested" reminder (strategy.scm).
(define (plan-language-auto-tested?)
  (and plan-language (pair? plan-spec) (> plan-language-auto-runs 0)))

;; New files the model writes that the plan names (a test file a Test:
;; step adds) are marked as intended for the next commit (git-intend), so a tool that lists the tracked files sees them: the
;; book's catalogue generator does, and on commit-bench (dsl8 107251d0) a
;; test file the model added was missing from the catalogue it had just
;; regenerated, which both reviewers found stale.
(define (plan-language-intend calls results)
  (let ((paths (let walk ((cs calls) (rs results) (out '()))
                 (if (or (null? cs) (null? rs))
                     out
                     (let* ((c (car cs)) (r (car rs))
                            (a (guard (e (else '())) (json-parse (format "~a" (assoc-ref c 'args "{}")))))
                            (p (if (pair? a) (format "~a" (assoc-ref a "path" "")) "")))
                       (walk (cdr cs) (cdr rs)
                             (if (and (equal? (assoc-ref c 'name "") "fs-write") (assoc-ref r 'ok #f)
                                      (not (string-empty? p)) (not (string-prefix? "$TMPDIR" p)) (not (string-prefix? "/" p))
                                      ;; Only what the plan asks for: a file a run left
                                      ;; behind stays untracked, for the new-files reminder.
                                      (guard (e (else #f)) (plan-names? p)))
                                 (cons p out)
                                 out)))))))
    (when (pair? paths)
      (let ((marked (guard (e (else '())) (git-intend paths))))
        (when (pair? marked)
          (log-note (string-append "plan language: new files marked for the next commit: " (string-join marked ", "))))))))

;; The plan's commands, run by the harness as the work goes (2026-10-13).
;; On commit-bench the plan's check, make validate, failed in every build's
;; attempts on a program the environment lacks (staticcheck: No such file or
;; directory), and the model spent up to 19 calls of an attempt on it; and to
;; see its new test fail without the change, it set the unchanged code up by
;; hand (git archive, the test file copied in, the test run: four calls in
;; dsl3's 556d6d12), which test-before does at no cost in tokens. So when a
;; run of the plan's check fails, the check runs once on a clone of the last
;; commit (check-before); a failing run whose failing lines are all among
;; that run's is told so, in its own result, and counts for the check step.
;; And when a planned test first passes, it runs on the unchanged code and
;; its result says whether it fails there.
(define plan-language-before #f)         ; check-before's alist, or 'none when it could not run
(define plan-language-as-before '())     ; the check's command lines whose latest run since the last edit failed only as before
(define plan-language-before-notes 2)    ; failing runs told so, per turn
(define plan-language-before-noted 0)
(define plan-language-tests-before '())  ; ((path run content) . status) of test-before runs
(define plan-language-started-ids '())   ; background runs: their ids (tool output, only compared) and command lines
(define plan-language-started-lines '())
(define plan-language-started-texts '()) ; what each has printed so far, read by read

;; The plan's check command, or #f.
(define (plan-language-check-run)
  (let find ((ss plan-spec))
    (cond ((null? ss) #f)
          ((equal? (assoc-ref (car ss) 'kind "") "Check")
           (let ((run (assoc-ref (car ss) 'run ""))) (and (not (string-empty? run)) run)))
          (else (find (cdr ss))))))

(define (plan-language-check-line? line)
  (let ((run (plan-language-check-run)))
    (and run line (string-contains? line run))))

;; The check on the unchanged code, run once a turn, when a run of it fails.
(define (plan-language-before-run)
  (or plan-language-before
      (let* ((run (plan-language-check-run))
             (r (and run (guard (e (else #f)) (check-before "bash" (list "-c" run))))))
        (set! plan-language-before (if (pair? r) r 'none))
        (log-note (if (pair? r)
                      (string-append "plan language: the check on the unchanged code exited "
                                     (number->string (assoc-ref r "status" 0)) ", "
                                     (number->string (length (assoc-ref r "lines" '()))) " failing lines")
                      "plan language: the check could not run on the unchanged code"))
        plan-language-before)))

;; Whether a failing run's output fails only as the check did on the
;; unchanged code: it has failing lines, and each is among that run's.
(define (plan-language-as-before? text)
  (let ((b plan-language-before))
    (and (pair? b)
         (not (equal? (assoc-ref b "status" 0) 0))
         (let ((known (assoc-ref b "lines" '()))
               (lines (guard (e (else '())) (failure-lines text))))
           (and (pair? lines)
                (let all ((ls lines)) (or (null? ls) (and (member (car ls) known) (all (cdr ls))))))))))

(define (plan-language-before-note)
  (string-append "\n[hint: the plan's check `" (plan-language-check-run)
                 "` fails on the unchanged code too (run on a clone of the last commit), with:"
                 (apply string-append (map (lambda (l) (string-append "\n  " l)) (assoc-ref plan-language-before "shown" '())))
                 "\nEvery failing line above is among those: the failure was there before your change (the environment or the code as it was), and a run that fails only this way counts as the check passing for your plan. If it stops the check before the check's later parts run, run those parts yourself.]"))

(define (plan-language-started-line id)
  (let find ((ids plan-language-started-ids) (lines plan-language-started-lines))
    (cond ((or (not id) (null? ids) (null? lines)) #f)
          ((equal? (car ids) id) (car lines))
          (else (find (cdr ids) (cdr lines))))))

;; An edit to the code: a repository file that is not a document.
(define (plan-language-code-edit? c r)
  (and (member (assoc-ref c 'name "") '("fs-edit" "fs-write")) (assoc-ref r 'ok #f)
       (let* ((a (guard (e (else '())) (json-parse (format "~a" (assoc-ref c 'args "{}")))))
              (p (if (pair? a) (format "~a" (assoc-ref a "path" "")) "")))
         (not (or (string-empty? p) (string-prefix? "$TMPDIR" p) (string-prefix? "/" p) (doc-path? p))))))

(define (plan-language-started-text id)
  (let find ((ids plan-language-started-ids) (ts plan-language-started-texts))
    (cond ((or (not id) (null? ids) (null? ts)) "")
          ((equal? (car ids) id) (car ts))
          (else (find (cdr ids) (cdr ts))))))

;; A background run's output arrives read by read: on commit-bench (dsl6,
;; 556d6d12) make validate's missing staticcheck was in a read that said the
;; run was still going, and the read that saw it exit held only its last
;; lines. So each run's output is kept as it comes, its first and last
;; 100,000 characters.
(define (plan-language-add-text! id more)
  (set! plan-language-started-texts
        (let walk ((ids plan-language-started-ids) (ts plan-language-started-texts))
          (cond ((or (null? ids) (null? ts)) '())
                ((equal? (car ids) id)
                 (let* ((t (string-append (car ts) "\n" more)) (n (string-length t)))
                   (cons (if (> n 200000) (string-append (substring t 0 100000) (substring t (- n 100000) n)) t) (cdr ts))))
                (else (cons (car ts) (walk (cdr ids) (cdr ts))))))))

(define (plan-language-before-round calls results)
  (for-each
   (lambda (c r)
     (let* ((name (assoc-ref c 'name ""))
            (shell? (member name '("shell-exec" "shell-start" "shell-read")))
            (status (cond ((equal? name "shell-exec") (run-status r))
                          ((member name '("shell-start" "shell-read")) (proc-exit-status r))
                          (else #f)))
            (line (cond ((member name '("shell-exec" "shell-start")) (command-line c))
                        ((equal? name "shell-read") (plan-language-started-line (proc-call-id c)))
                        (else #f)))
            (check? (and line (plan-language-check-line? line)))
            (out (if shell? (plan-language-output r) ""))
            (value (format "~a" (assoc-ref r 'value ""))))
       (when (and (equal? name "shell-start") (not status) (proc-id r))
         (set! plan-language-started-ids (cons (proc-id r) plan-language-started-ids))
         (set! plan-language-started-lines (cons (command-line c) plan-language-started-lines))
         (set! plan-language-started-texts (cons out plan-language-started-texts)))
       (when (and (equal? name "shell-read") (proc-call-id c))
         (plan-language-add-text! (proc-call-id c) out))
       (when (plan-language-code-edit? c r)
         (set! plan-language-as-before '()))
       ;; A missing program is told as soon as a result shows it, whether
       ;; the run has ended or not.
       (when shell?
         (let* ((missing (guard (e (else '())) (missing-programs out)))
                (new (filter (lambda (p) (not (member p plan-language-missing-told))) missing)))
           (when (pair? new)
             (set! plan-language-missing-told (append new plan-language-missing-told))
             (fold-result (assoc-ref c 'id) (string-append value (plan-language-missing-note new check?))))))
       (when (and (integer? status) check?)
         (set! plan-language-as-before (filter (lambda (l) (not (equal? l line))) plan-language-as-before)))
       (when (and (integer? status) (not (= status 0)) line)
         (let ((text (if (equal? name "shell-read") (plan-language-started-text (proc-call-id c)) out)))
           (cond ((pair? (guard (e (else '())) (missing-programs text)))
                  ;; A program the environment lacks: the run says nothing
                  ;; of the change past where it stopped.
                  (when check? (set! plan-language-as-before (cons line plan-language-as-before))))
                 (else
                  (when check? (plan-language-before-run))
                  (when (plan-language-as-before? text)
                    (when check? (set! plan-language-as-before (cons line plan-language-as-before)))
                    (when (< plan-language-before-noted plan-language-before-notes)
                      (set! plan-language-before-noted (+ plan-language-before-noted 1))
                      (fold-result (assoc-ref c 'id) (string-append value (plan-language-before-note)))))))))))
   calls results))

;; The programs a failing run said are not installed, told once each a turn.
;; On commit-bench every build's attempts met make validate's missing
;; staticcheck and spent up to 19 calls finding, building or installing it,
;; at best to see the rest of validate pass (2026-10-13).
(define plan-language-missing-told '())

(define (plan-language-missing-note progs check?)
  (let ((names (string-join (map (lambda (p) (string-append "`" p "`")) progs) ", "))
        (them (if (pair? (cdr progs)) "them" "it")))
    (string-append "\n[hint: " names (if (pair? (cdr progs)) " are" " is") " not installed here, so the run stopped where it needed " them
                   ". Do not spend calls installing or building " them
                   ": if the run stopped before the rest of what it checks (for a make target, the recipe's later lines), run those parts yourself, and say in your report that "
                   names " could not run here."
                   (if check? " The plan counts a run of its check that stops only on this as passing." "") "]")))

;; What a run printed: shell-exec's stdout and stderr, from the JSON its
;; result is, or a background run's result as it reads.
(define (plan-language-output r)
  (let* ((value (format "~a" (assoc-ref r 'value "")))
         (v (guard (e (else #f)) (json-parse value))))
    (if (pair? v)
        (string-append (format "~a" (assoc-ref v "stdout" "")) "\n" (format "~a" (assoc-ref v "stderr" "")))
        value)))

;; A planned test that says it fails today, run on the unchanged code
;; (test-before), once for each version of its file: its exit status, or #f
;; when it could not run there.
(define (plan-language-test-before st)
  (let* ((path (assoc-ref st 'path "")) (run (assoc-ref st 'run ""))
         (content (guard (e (else "")) (format "~a" (fs-read path))))
         (key (list path run content))
         (hit (assoc key plan-language-tests-before)))
    (if hit
        (cdr hit)
        (let ((status (guard (e (else #f)) (test-before path "bash" (list "-c" run)))))
          (set! plan-language-tests-before (cons (cons key status) plan-language-tests-before))
          status))))

;; What a planned test's first pass is told: whether it fails on the
;; unchanged code, as a test of the change does.
(define (plan-language-test-note st)
  (and (string-prefix? "fails today" (assoc-ref st 'today ""))
       (not (string-empty? (assoc-ref st 'run "")))
       (let ((status (plan-language-test-before st))
             (where (string-append " (`" (assoc-ref st 'run "") "` on a copy of the last commit with your test file in it)")))
         (cond ((not (integer? status)) #f)
               ((= status 0)
                (string-append "\n[hint: " (assoc-ref st 'name "") " passes on the unchanged code too" where
                               ", so it does not show the change: make it fail there.]"))
               (else
                (string-append "\n[hint: " (assoc-ref st 'name "") " fails on the unchanged code" where
                               ", as the plan says: no need to check that yourself.]"))))))

;; A run of the plan's check that failed only as it did on the unchanged
;; code is no failing run for the reminders: what it failed on was there
;; before the change.
(define plan-language-base-run-outcome run-outcome)
(define (run-outcome call result last)
  (let ((o (plan-language-base-run-outcome call result last)))
    (if (and (eq? o 'failed) plan-language (pair? plan-spec)
             (member (command-line call) plan-language-as-before))
        'passed
        o)))

;; The planner's instructions: the forms, the rule on names, and the rest of
;; plan-shape's guidance on behaviour and open choices.
(define plan-language-shape
  (string-append
   "Write the plan as Scheme data: one form per step, in the order the work goes, and nothing else (no prose, no numbering, no code fences). The forms:"
   "\n(change \"path\" \"function\" \"what changes in it, at every place the change must reach\") — \"\" for the function when the change is not in one"
   "\n(test \"path\" \"TestName\" \"the behaviour it checks, with an input that tells it apart from the nearest wrong behaviour\" (fails-today \"how it fails on the unchanged code\") (run \"the command that runs it\")) — (passes-today \"what it guards\") for a test of behaviour that already works"
   "\n(docs \"path\" \"what the change makes wrong or incomplete in it\") — a document that describes the behaviour the change alters, never a ticket or issue number the request or the repository does not give"
   "\n(check \"the command that shows the whole change works\") — the last step"
   "\n(none test \"why there is no test\") or (none docs \"why no document changes\")"
   "\n(names \"name\" ...) — every name the request gives for something the change must write into a file (a header, a flag, a field, a new function, a key), spelled as the request spells it; not a function or file the change only edits, not a phrase or a message the request quotes, and not words of the request's instructions to you (such as a line to end your answer with)"
   "\n(context \"name\" ...) — the other names the request gives that read as code: what the change only uses or edits; nothing else"
   "\nEvery name of the request that reads as code (session-id, cache_key, output_index) appears in names or context. If you think one is already in the code under another name, keep it in names anyway: the change is checked for the request's own spelling."
   "\nSay what the code should do as behaviour, in which cases and with what result, not as code to type. Where the request or the code leaves a detail open (which helper to call, a literal's format, which of several fixes), name the options and what will decide between them inside the step, and leave the choice to whoever carries the plan out. Copy the request's own examples, names and formats word for word."
   "\nFor example:"
   "\n(change \"src/flags.go\" \"parseFlags\" \"accept --verbose and set Verbose\")"
   "\n(test \"src/flags_test.go\" \"TestVerboseFlag\" \"--verbose sets Verbose and -q does not\" (fails-today \"the flag is unknown\") (run \"go test ./src -run TestVerboseFlag\"))"
   "\n(docs \"README.md\" \"the flags section lists --verbose\")"
   "\n(names \"--verbose\" \"Verbose\")"
   "\n(check \"go test ./...\")"))

;; The planner's request for a plan, and for a better one.
(define plan-language-base-draft plan-draft-prompt)
(define (plan-draft-prompt request)
  (if plan-language
      (string-append "Write a short, concrete plan for the following request. " plan-language-shape "\n\nRequest:\n" request)
      (plan-language-base-draft request)))

(define plan-language-base-revise plan-revise-prompt)
(define (plan-revise-prompt request plan score)
  (if plan-language
      (string-append "Your plan for the request below scored " (format-score score)
                     " out of 1 for completeness, concreteness and order. Revise it: fix what is missing or vague, such as a test that does not say why it fails on the unchanged code or a doc the change makes wrong, and remove what is unnecessary. "
                     plan-language-shape "\n\nRequest:\n" request "\n\nPrevious plan:\n" plan)
      (plan-language-base-revise request plan score)))

(define plan-language-base-best plan-best)
(define (plan-best judged request decision . seen)
  (set! plan-language-request (if (pair? seen) (car seen) request))
  (apply plan-language-base-best judged request decision seen))

;; A form's head as a string, or #f.
(define (plan-form-head form)
  (and (pair? form) (symbol? (car form)) (symbol->string (car form))))

;; The strings among a form's arguments, in order, and its sub-forms.
(define (plan-form-strings form) (filter string? (cdr form)))
;; A sub-form found at any depth: a planner wrote (run ...) inside
;; (fails-today ...) on commit-bench (556d6d12), and the reply did not read.
(define (plan-form-sub form head)
  (let find ((xs (cdr form)))
    (cond ((null? xs) #f)
          ((equal? (plan-form-head (car xs)) head)
           (let ((s (plan-form-strings (car xs)))) (if (pair? s) (car s) "")))
          (else (let ((inner (and (pair? (car xs)) (plan-form-head (car xs)) (plan-form-sub (car xs) head))))
                  (or inner (find (cdr xs))))))))

(define (plan-nth xs n) (if (> (length xs) n) (list-ref xs n) ""))

;; One form as a step alist, or a string saying what is wrong with it.
(define (plan-language-step form i)
  (let* ((head (plan-form-head form))
         (s (if head (plan-form-strings form) '()))
         (where (string-append "form " (number->string i))))
    (cond ((not head) (string-append where " is not a list that starts with change, test, docs, check, none, names or context"))
          ((equal? head "change")
           (if (null? s) (string-append where ": change needs its file")
               (list (cons 'kind "Change") (cons 'path (car s))
                     (cons 'name (if (> (length s) 2) (cadr s) ""))
                     (cons 'what (if (> (length s) 2) (caddr s) (plan-nth s 1))))))
          ((equal? head "docs")
           (if (null? s) (string-append where ": docs needs its file")
               (list (cons 'kind "Docs") (cons 'path (car s)) (cons 'name "") (cons 'what (plan-nth s 1)))))
          ((equal? head "test")
           (let ((fails (plan-form-sub form "fails-today")) (passes (plan-form-sub form "passes-today"))
                 (run (plan-form-sub form "run")))
             (cond ((< (length s) 2) (string-append where ": test needs its file and its name"))
                   ((not (or fails passes)) (string-append where ": test " (cadr s) " needs (fails-today \"...\") or (passes-today \"...\")"))
                   ((not run) (string-append where ": test " (cadr s) " needs (run \"the command that runs it\")"))
                   (else (list (cons 'kind "Test") (cons 'path (car s)) (cons 'name (cadr s)) (cons 'what (plan-nth s 2))
                               (cons 'today (if fails (string-append "fails today: " fails) (string-append "passes today: " passes)))
                               (cons 'run run))))))
          ((equal? head "check")
           (let ((run (or (and (pair? s) (car s)) (plan-form-sub form "run"))))
             (if (or (not run) (string-empty? run)) (string-append where ": check needs its command")
                 (list (cons 'kind "Check") (cons 'path "") (cons 'name "") (cons 'what "") (cons 'run run)))))
          ((equal? head "none")
           (let ((of (and (pair? (cdr form)) (symbol? (cadr form)) (symbol->string (cadr form)))))
             (if (member of '("test" "docs"))
                 (list (cons 'kind (if (equal? of "test") "Test" "Docs")) (cons 'none #t) (cons 'path "") (cons 'name "")
                       (cons 'what (if (pair? s) (car s) "")))
                 (string-append where ": none takes test or docs, then why"))))
          ((member head '("names" "context")) (list (cons 'kind head) (cons 'list s)))
          (else (string-append where " starts with " head ", which is none of change, test, docs, check, none, names or context")))))

;; A plan's text read as data: (steps names context problems), or #f when it
;; is not readable as forms at all.
;; A chunk of a plan read with up to three closing parentheses added, or #f.
(define (plan-language-read-chunk t)
  (let try ((t t) (left 3))
    (or (guard (e (else #f)) (read-data t))
        (and (> left 0) (try (string-append t ")") (- left 1))))))

;; A reply's forms: read whole, or else form by form, a form being a line
;; that starts with "(" and the indented lines after it, each forgiven a
;; missing closing parenthesis or three: the planner of commit-bench's
;; 556d6d12 wrote (run ...) inside (fails-today ...) and closed its test one
;; parenthesis short, and the whole reply did not read.
(define (plan-language-forms text)
  (or (guard (e (else #f)) (read-data text))
      (let walk ((lines (string-split text "\n")) (chunk "") (out '()))
        (let ((flush (lambda () (if (string-empty? (string-trim chunk)) '() (or (plan-language-read-chunk chunk) #f)))))
          (cond ((null? lines)
                 (let ((last (flush))) (and last (append out last))))
                ((string-prefix? "(" (car lines))
                 (let ((done (flush)))
                   (and done (walk (cdr lines) (car lines) (append out done)))))
                (else (walk (cdr lines) (string-append chunk "\n" (car lines)) out)))))))

(define (plan-language-read text)
  (let ((forms (plan-language-forms text)))
    (and forms (pair? forms) (pair? (car forms))
         (let walk ((fs forms) (i 1) (steps '()) (names '()) (context '()) (problems '()))
           (if (null? fs)
               (list (reverse steps) names context (reverse problems))
               (let ((st (plan-language-step (car fs) i)))
                 (cond ((string? st) (walk (cdr fs) (+ i 1) steps names context (cons st problems)))
                       ((equal? (assoc-ref st 'kind "") "names") (walk (cdr fs) (+ i 1) steps (append names (assoc-ref st 'list '())) context problems))
                       ((equal? (assoc-ref st 'kind "") "context") (walk (cdr fs) (+ i 1) steps names (append context (assoc-ref st 'list '())) problems))
                       (else (walk (cdr fs) (+ i 1) (cons st steps) names context problems)))))))))

;; Every string a plan holds, for the names check.
(define (plan-language-said steps names context)
  (string-join (append (map (lambda (st) (string-append (assoc-ref st 'path "") " " (assoc-ref st 'name "") " " (assoc-ref st 'what "")
                                                       " " (assoc-ref st 'run "") " " (assoc-ref st 'today ""))) steps)
                       names context)
               "\n"))

;; The request's own text inside a run's turn prompt, or the request.
(define (plan-language-task request)
  (let* ((parts (string-split request "\n\nWork on the plan's next unmarked step")))
    (car parts)))

;; The request's names a plan holds nowhere.
(define (plan-language-absent steps names context)
  (let ((said (plan-language-said steps names context))
        (asked (guard (e (else '())) (request-names (plan-language-task plan-language-request)))))
    (filter (lambda (n) (not (string-contains? said n))) asked)))

;; A read plan with the request's names it left out that the code already
;; has put in its context: such a name is one the change uses, and asking the
;; planner for it again cost a reply in every attempt on commit-bench (dsl3
;; and dsl6, gpt-6.1-sol, api.openai.com: 2026-10-13). A name the code lacks
;; is still asked for: session-id, which planners took for the X-Session-Id
;; the code had, is not in it whole.
(define (plan-language-with-known read)
  (let* ((absent (plan-language-absent (car read) (cadr read) (caddr read)))
         (known (filter (lambda (n) (guard (e (else #f)) (head-has-name n))) absent)))
    (when (pair? known)
      (log-note (string-append "plan language: names the code already has, put in the plan's context: " (string-join known ", "))))
    (list (car read) (cadr read) (append (caddr read) known) (list-ref read 3))))

;; What is wrong with a read plan as a whole.
(define (plan-language-problems steps names context)
  (let* ((kinds (map (lambda (st) (assoc-ref st 'kind "")) steps))
         (said (plan-language-said steps names context))
         (asked (guard (e (else '())) (request-names (plan-language-task plan-language-request))))
         (absent (filter (lambda (n) (not (string-contains? said n))) asked)))
    (append
     (if (member "Change" kinds) '() '("there is no change step"))
     (if (member "Test" kinds) '() '("there is no test step: add one, or (none test \"why\")"))
     (if (member "Docs" kinds) '() '("there is no docs step: add one, or (none docs \"why\")"))
     (if (and (pair? kinds) (equal? (car (reverse kinds)) "Check")) '() '("the last step is not a check"))
     (if (null? absent) '()
         (list (string-append "the request gives these names and the plan has none of them: "
                              (string-join (map (lambda (n) (string-append "\"" n "\"")) absent) ", ")
                              "; put each in (names ...) if the change produces it, or in (context ...) if it only uses it"))))))

;; The plan written out as numbered steps, as the window and the console show
;; a plan, with the names after them.
(define (plan-language-text steps names context)
  (let walk ((ss steps) (i 1) (out '()))
    (if (null? ss)
        (string-join (append (reverse out)
                             (if (pair? names) (list (string-append "Names the change produces, as the request spells them: "
                                                                    (string-join (map (lambda (n) (string-append "\"" n "\"")) names) ", ") ".")) '())
                             (if (pair? context) (list (string-append "Names it only uses: " (string-join context ", ") ".")) '())
                             (if (let any ((ss steps)) (and (pair? ss) (or (equal? (assoc-ref (car ss) 'kind "") "Test") (any (cdr ss)))))
                                 (list "After each of your edits to the code the harness runs the plan's tests (each test's command, once its file has the test) and adds how they ran to that edit's result: run them yourself only to see more than it shows.")
                                 '()))
                     "\n")
        (let* ((st (car ss)) (kind (assoc-ref st 'kind "")) (n (number->string i))
               (line (cond ((assoc-ref st 'none #f) (string-append n ". " kind ": none — " (assoc-ref st 'what "")))
                           ((equal? kind "Check") (string-append n ". Check: " (assoc-ref st 'run "")))
                           (else (string-append n ". " kind ": " (assoc-ref st 'path "")
                                                (let ((nm (assoc-ref st 'name ""))) (if (string-empty? nm) "" (string-append " " nm)))
                                                " — " (assoc-ref st 'what "")
                                                (let ((t (assoc-ref st 'today ""))) (if (string-empty? t) "" (string-append "; " t)))
                                                (let ((r (assoc-ref st 'run ""))) (if (string-empty? r) "" (string-append "; run: " r))))))))
          (walk (cdr ss) (+ i 1) (cons line out))))))

;; The planner's reply as a plan: the forms read, checked and written out,
;; asked again with what is wrong at most plan-language-retries times. A
;; reply never readable as forms is kept as the prose it is.
(define plan-language-base-ask plan-ask)
(define (plan-ask prompt)
  (if (not plan-language)
      (plan-language-base-ask prompt)
      (let ask ((text (plan-language-base-ask prompt)) (left plan-language-retries))
        (let* ((read (let ((r (plan-language-read text))) (and r (plan-language-with-known r))))
               (forms? (string-prefix? "(" (string-trim text)))
               (problems (if read
                             (append (list-ref read 3) (plan-language-problems (car read) (cadr read) (caddr read)))
                             (list "it is not readable as the forms below"))))
          ;; A reply in prose is a plan in prose: kept as it is, as before.
          ;; One that starts as forms and does not read, or reads with
          ;; problems, is asked for again.
          (cond ((and (pair? problems) (> left 0) (or read forms?))
                 (log-note (string-append "plan language: asking the planner again: " (string-join problems "; ")))
                 (ask (plan-language-base-ask (string-append prompt "\n\nYour plan was:\n" text "\n\nIt cannot be used as it is: "
                                                             (string-join problems "; ") ". Write it again, as the forms only."))
                      (- left 1)))
                ((not read)
                 (log-note "plan language: the planner's plan is not readable as forms; it is kept as prose")
                 text)
                (else
                 (set! plan-spec (car read))
                 (set! plan-spec-names (cadr read))
                 (set! plan-spec-context (caddr read))
                 (when (pair? problems)
                   (log-note (string-append "plan language: kept with problems: " (string-join problems "; "))))
                 (plan-language-text (car read) (cadr read) (caddr read))))))))

;; A plan the model writes with plan-write may be forms too.
(define plan-language-base-write plan-write-answer)
(define (plan-write-answer call args)
  (let* ((text (string-trim (format "~a" (assoc-ref args "plan" ""))))
         (read (and plan-language (plan-language-read text))))
    (if (and read (pair? (car read)) (null? (list-ref read 3)))
        (begin (set! plan-spec (car read))
               (set! plan-spec-names (cadr read))
               (set! plan-spec-context (caddr read))
               (plan-language-base-write call (list (cons "plan" (plan-language-text (car read) (cadr read) (caddr read))))))
        (let ((answer (plan-language-base-write call args)))
          ;; A plan the model writes in prose replaces the planner's forms:
          ;; set against the old forms, its steps were said to lack evidence
          ;; they did not need (commit-bench, dsl8 107251d0: a rewritten plan
          ;; of 7 steps, told of the forms' step 8). The prose plan is kept
          ;; as prose plans are, with the generic checks.
          (when (and (pair? plan-spec) (string-prefix? "Plan recorded" (format "~a" answer)))
            (log-note "plan language: the model wrote its plan in prose; the planner's forms no longer stand for it")
            (set! plan-spec '())
            (set! plan-spec-names '())
            (set! plan-spec-context '()))
          answer))))

;; Whether a command the turn ran and saw pass since the last edit (passed)
;; covers cmd: it contains it, or it is the repository's whole check.
(define (plan-language-passed? passed what)
  (let any ((ps passed))
    (and (pair? ps)
         (or (and (not (string-empty? what)) (string-contains? (car ps) what))
             (any (cdr ps))))))

(define (plan-language-whole? passed)
  (let any ((ps passed))
    (and (pair? ps)
         (or (let w ((ws whole-check-words)) (and (pair? ws) (or (string-contains? (car ps) (car ws)) (w (cdr ws)))))
             (any (cdr ps))))))

;; The plan's steps that lack their evidence, as lines, and the steps that
;; have it, as (number . note), set against the change (plan-evidence) and
;; the commands that passed since the last edit (passed).
(define (plan-language-check passed)
  (let* ((paths (map (lambda (st) (assoc-ref st 'path ""))
                     (filter (lambda (st) (and (member (assoc-ref st 'kind "") '("Change" "Docs" "Test"))
                                               (not (assoc-ref st 'none #f))
                                               (not (string-empty? (assoc-ref st 'path "")))))
                             plan-spec)))
         (ev (guard (e (else '())) (plan-evidence plan-spec-names paths)))
         (missing (assoc-ref ev "missing" '()))
         (unchanged (assoc-ref ev "unchanged" '()))
         (whole (plan-language-whole? passed)))
    (let walk ((ss plan-spec) (i 1) (unmet '()) (met '()))
      (if (null? ss)
          (list (append (reverse unmet)
                        (if (pair? missing)
                            (list (string-append "the change does not contain "
                                                 (string-join (map (lambda (n) (string-append "\"" n "\"")) missing) ", ")
                                                 ", which the plan says it produces"))
                            '())
                        (plan-language-docs-unmet))
                (reverse met))
          (let* ((st (car ss)) (kind (assoc-ref st 'kind "")) (path (assoc-ref st 'path "")) (name (assoc-ref st 'name ""))
                 (label (string-append "step " (number->string i) " (" kind (if (string-empty? path) "" (string-append " " path))
                                       (if (string-empty? name) "" (string-append " " name)) ")"))
                 (problem
                  (cond ((assoc-ref st 'none #f) #f)
                        ((member kind '("Change" "Docs"))
                         (and (member path unchanged) (string-append label ": the file is unchanged")))
                        ((equal? kind "Test")
                         (cond ((let ((text (guard (e (else "")) (format "~a" (fs-read path)))))
                                  (not (string-contains? text name)))
                                (string-append label ": the test is not in that file"))
                               ((not (or (plan-language-passed? passed name) (plan-language-passed? passed (assoc-ref st 'run "")) whole
                                         (member name plan-language-auto-passed)))
                                (string-append label ": it has not passed since your last edit"))
                               ((plan-language-passes-before? st)
                                (string-append label ": it passes on the unchanged code too (`" (assoc-ref st 'run "")
                                               "` on a copy of the last commit with your test file in it), so it does not show the change: make it fail there"))
                               (else #f)))
                        ((equal? kind "Check")
                         (and (not (or (plan-language-passed? passed (assoc-ref st 'run "")) whole
                                       (plan-language-passed? plan-language-as-before (assoc-ref st 'run ""))))
                              (plan-language-final-check st label)))
                        (else #f))))
            (if problem
                (walk (cdr ss) (+ i 1) (cons problem unmet) met)
                (walk (cdr ss) (+ i 1) unmet (cons (cons i (if (assoc-ref st 'none #f) "none" (string-append "evidence: " kind " step met"))) met))))))))

;; A check step not seen to pass since the last edit, run by the harness
;; before the turn ends: #f when it passes there, or fails only on a program
;; the environment lacks or only as it did on the unchanged code; else what
;; is missing, with the end of its output. On commit-bench (dsl6, 556d6d12)
;; the model's own make validate printed its PASS, and the model read no
;; further than that: the run was not seen to exit, and the model was asked
;; to run it again.
(define plan-language-final-budget 900)

(define (plan-language-final-check st label)
  (let* ((run (assoc-ref st 'run ""))
         (r (guard (e (else #f)) (plan-run "bash" (list "-c" run) plan-language-final-budget)))
         (status (and (pair? r) (assoc-ref r "status" #f)))
         (tail (if (pair? r) (format "~a" (assoc-ref r "tail" "")) "")))
    (log-note (string-append "plan language: the harness ran the check before the turn ended: "
                             (if (integer? status) (string-append "exit " (number->string status)) "it could not run")))
    (cond ((not (integer? status)) (string-append label " " run ": it has not passed since your last edit"))
          ((= status 0) #f)
          ((pair? (guard (e (else '())) (missing-programs tail))) #f)
          ((begin (plan-language-before-run) (plan-language-as-before? tail)) #f)
          (else (string-append label " " run ": it fails when the harness runs it now (exit " (number->string status) "):\n"
                               (plan-language-last-lines tail 15))))))

;; Documents that describe what the change alters, when the change edited
;; none: the repository's documents (docs-mentioning) holding a function the
;; plan's change steps name, and the ADRs named for a package they change
;; (docs/adr/0025-ingress-redaction.md for internal/redact), unchanged by the
;; change, said once as one item.
;; On commit-bench (dsl9 3422faf1) the planner wrote (none docs ...) and the
;; executor followed it; the reference, and the build without plans, amended
;; ADR 25, which describes the Scanner.Scan and TextSecret the change altered.
(define (plan-language-docs-unmet)
  (if (let any ((ps plan-language-edited))
        (and (pair? ps) (or (and (doc-path? (car ps)) (not (string-contains? (car ps) "catalogue"))) (any (cdr ps)))))
      ;; The change has a document of its own: which ones it needed is the
      ;; work's to judge, and naming others costs reads (556d6d12 amends ADR
      ;; 9; ADR 8 is named for its llm package).
      '()
      (plan-language-docs-candidates)))

(define (plan-language-docs-candidates)
  (let* ((names (filter (lambda (n) (>= (string-length n) 4))
                        (map (lambda (st) (assoc-ref st 'name ""))
                             (filter (lambda (st) (equal? (assoc-ref st 'kind "") "Change")) plan-spec))))
         (packages (let walk ((ss plan-spec) (out '()))
                     (if (null? ss)
                         (reverse out)
                         (let* ((st (car ss)) (path (assoc-ref st 'path ""))
                                (parts (string-split path "/"))
                                (dir (if (and (equal? (assoc-ref st 'kind "") "Change") (> (length parts) 1))
                                         (list-ref parts (- (length parts) 2))
                                         #f)))
                           (walk (cdr ss) (if (and dir (not (member dir out))) (cons dir out) out))))))
         (docs (if (or (pair? names) (pair? packages)) (guard (e (else '())) (docs-mentioning names packages)) '()))
         (unchanged (if (pair? docs)
                        (assoc-ref (guard (e (else '())) (plan-evidence '() (map car docs))) "unchanged" '())
                        '()))
         (left (filter (lambda (d) (member (car d) unchanged)) docs)))
    (if (null? left)
        '()
        (list (string-append "documents that describe what your change alters are unchanged: "
                             (string-join (map (lambda (d) (string-append (car d) " (" (cdr d) ")")) left) "; ")
                             ". Read what each says of it, and amend what no longer holds, or say in your report why it still holds")))))

;; Whether a test step that says it fails today passes on the code as it
;; was (test-before): #f when it fails there, as it should, when it could not
;; run there (a not-found exit), or when the step says it passes today.
(define (plan-language-passes-before? st)
  (and (string-prefix? "fails today" (assoc-ref st 'today ""))
       (not (string-empty? (assoc-ref st 'run "")))
       (equal? (plan-language-test-before st) 0)))

;; Mark the steps that have their evidence, so the model need not.
(define (plan-language-mark met)
  (for-each (lambda (m) (plan-language-mark-one (car m) (cdr m))) met))

(define (plan-language-unmet-message unmet)
  (string-append "Before you finish, your plan's evidence is missing:"
                 (apply string-append (map (lambda (u) (string-append "\n- " u)) unmet))
                 "\nDo what is missing, or say in your report why a step does not apply."))
