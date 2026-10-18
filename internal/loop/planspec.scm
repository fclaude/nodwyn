;; A plan as data (ADR 32, amended 2026-10-16). The planner writes its plan
;; as Scheme forms, one per step, instead of sentences:
;;
;;   (change "src/flags.py" "parse_flags" "accept --verbose and set verbose")
;;   (test "tests/test_flags.py" "test_verbose_flag" "--verbose sets verbose"
;;         (fails-today "the flag is unknown") (run "pytest tests/test_flags.py -k test_verbose_flag"))
;;   (docs "README.md" "the flags section lists --verbose")
;;   (names "--verbose" "verbose")
;;   (assumes "-q wins when both flags are given" "test_flags_last_wins expects the same")
;;   (check "pytest")
;;
;; The example is Python's, and the form is the same in any language: the
;; test's run is the repository's own command for it (go test ./src -run
;; TestVerboseFlag, pytest tests/test_x.py::test_y, npm test -- x.test.js).
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

;; Reset at turn-start, which the host calls before nodwyn-turn, so a project
;; that redefines nodwyn-turn keeps the reset (strategy.scm).
(define plan-language-base-turn-start turn-start)
(define (turn-start request)
  (set! plan-spec '())
  (set! plan-spec-names '())
  (set! plan-spec-context '())
  (set! plan-language-marked '())
  (set! plan-language-edited '())
  (set! plan-language-tests-before '())
  (set! plan-language-untracked-before
        (guard (e (else '()))
          (map git-state-path (filter (lambda (l) (string-prefix? "?? " l)) (or (git-state) '())))))
  (plan-language-base-turn-start request))

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

;; Words of a test command that name the program or the way it is run, not
;; what it runs.
(define plan-run-launchers
  '("python" "python3" "uv" "run" "poetry" "env" "bash" "sh" "-c" "pytest" "unittest" "tox" "nox" "go" "test"
    "cargo" "npm" "yarn" "pnpm" "make" "jest" "vitest" "mocha" "rspec" "rake" "mvn" "gradle" "gradlew" "dotnet"
    "phpunit" "mix" "swift" "ctest" "runtests.py" "runtests"))

;; What a command runs: its words but for flags, numbers (a flag's value, `-v
;; 2`) and launchers, so `pytest -v 1 tests/test_a.py::test_x` and `python -m
;; pytest tests/test_a.py::test_x -rA` run the same thing.
(define (plan-run-targets line)
  (filter (lambda (w) (and (not (string-empty? w)) (not (string-prefix? "-" w)) (not (string->number w)) (not (member w plan-run-launchers))))
          (string-split line " ")))

;; Whether a command line the turn ran is the run the plan names: it contains
;; it, or it is a test run of everything the plan's command runs, whatever
;; flags either has. On SWE-bench (pytest-5787, sklearn-13124) the review said
;; the plan's check "has not passed since your last edit" for a run that
;; differed in `-v 1` against `-v 2`, and the model ran it again (ADR 32,
;; amended 2026-10-16).
(define (plan-language-ran? line run)
  (and (not (string-empty? run))
       (or (string-contains? line run)
           (let ((want (plan-run-targets run)))
             (and (pair? want)
                  (let ((have (plan-run-targets line)))
                    (let all ((ws want)) (or (null? ws) (and (member (car ws) have) (all (cdr ws)))))))))))

;; The package directory of a Go test file ("internal/log" for
;; internal/log/log_test.go, "" at the root), or #f for another file.
(define (plan-language-go-test-dir path)
  (and (string-suffix? "_test.go" path)
       (let ((parts (string-split (if (string-prefix? "./" path) (substring path 2 (string-length path)) path) "/")))
         (string-join (reverse (cdr (reverse parts))) "/"))))

;; A test step may name a scenario or fixture file instead of a test: a path
;; through a scenarios, testdata or fixtures folder (test/exfil/scenarios/019).
;; The test that runs it is an existing one, so its name is in no file; the
;; step is met by the file, one the repository has or one the turn wrote whose
;; name starts with the step's path, and by a run of the tests of the folder
;; above. On commit-bench (dsl20r, 3422faf1) the plan named
;; TestExfilScenario019 for a scenario file; the gate wanted that test in the
;; file, and the run refactored a test file, regenerated the catalogue and ran
;; validate again: 21 calls, 22% of the run (ADR 32, amended 2026-10-16).
(define plan-language-scenario-folders '("scenarios" "scenario" "testdata" "fixtures" "golden" "cases"))

;; The folder above a scenario folder in path ("test/exfil" for
;; test/exfil/scenarios/019, "" at the root), or #f for another path.
(define (plan-language-scenario-dir path)
  (let walk ((parts (string-split path "/")) (above '()))
    (cond ((null? parts) #f)
          ((member (car parts) plan-language-scenario-folders) (string-join (reverse above) "/"))
          (else (walk (cdr parts) (cons (car parts) above))))))

(define (plan-language-file? path)
  (and (repo-relative? path)
       (guard (e (else #f)) (fs-read path) #t)))

;; Whether a test step's test is where the step says: its name in its file,
;; or, for a scenario file, the file.
(define (plan-language-test-present? st)
  (let ((path (assoc-ref st 'path "")) (name (assoc-ref st 'name "")))
    (or (and (repo-relative? path) (string-contains? (guard (e (else "")) (format "~a" (fs-read path))) name))
        (and (plan-language-scenario-dir path)
             (or (plan-language-file? path)
                 (let any ((ps plan-language-edited))
                   (and (pair? ps)
                        (or (and (string-prefix? path (car ps)) (plan-language-file? (car ps)))
                            (any (cdr ps))))))))))

;; Whether shown, the passing lines of a run (run-entry), says the package in
;; dir passed: go test's "ok  <tab>module/dir<tab>...", unless it ran no test
;; ("[no tests to run]", a -run that named none).
(define (plan-language-package-ok? shown dir)
  (let any ((ls (string-split shown "\n")))
    (and (pair? ls)
         (or (let ((l (car ls)))
               (and (string-prefix? "ok  \t" l) (not (string-contains? l "[no tests to run]"))
                    (or (string-empty? dir) (string-contains? l (string-append "/" dir "\t")) (string-suffix? (string-append "/" dir) l))))
             (any (cdr ls))))))

;; Whether one of the runs that passed (run-entry's) is a test step's: one
;; that names its test or runs its command, or a go test of its package with
;; no -run (the go-test tool's). A Go test's run counts only when its output
;; shows the test, or its package, passing: an exit status alone marked steps
;; 3-6 of 3422faf1 from a run whose tests had failed (commit-bench, dsl18m,
;; 2026-10-14; ADR 32, amended 2026-10-14).
(define (plan-language-test-passed? st passed)
  (let* ((name (assoc-ref st 'name "")) (run (assoc-ref st 'run ""))
         (dir (plan-language-go-test-dir (assoc-ref st 'path "")))
         (scenarios (plan-language-scenario-dir (assoc-ref st 'path ""))))
    (let any ((ps passed))
      (and (pair? ps)
           (let ((line (car (car ps))) (shown (cdr (car ps))))
             (or (and (or (and (not (string-empty? name)) (string-contains? line name))
                          (plan-language-ran? line run))
                      (or (not dir)
                          (and (not (string-empty? name)) (string-contains? shown (string-append "--- PASS: " name)))
                          (plan-language-package-ok? shown dir)))
                 (and dir (string-contains? line "go test") (not (string-contains? line "-run"))
                      (plan-language-package-ok? shown dir))
                 ;; A scenario step: the tests of the folder above its folder
                 ;; passing run it.
                 (and scenarios (string-contains? line "go test") (not (string-contains? line "-run"))
                      (plan-language-package-ok? shown scenarios))
                 (any (cdr ps))))))))

;; Marks as the work happens: after each round of the main context's calls,
;; a change or docs step whose file a call wrote, a test step whose file was
;; written and whose test a run that passed ran (plan-language-test-passed?),
;; and a check step whose command passed. A run is one of the repository's
;; tools as the command it runs (tool-call), and passed as run-entry says:
;; not on the copy of the last commit, and not on a status a pipe or an ||
;; made 0. The marks are the console's progress; what counts at the end is
;; plan-language-check, after the last edit.
(define (plan-language-round calls results)
  (let walk ((cs calls) (rs results))
    (when (and (pair? cs) (pair? rs))
      (let* ((c0 (car cs)) (r0 (car rs)) (c (tool-call c0)) (r (tool-result c0 r0)) (name (assoc-ref c 'name ""))
             (a (guard (e (else '())) (json-parse (format "~a" (assoc-ref c 'args "{}")))))
             (entry (run-entry c r)))
        (cond ((and (member name '("fs-edit" "fs-write")) (assoc-ref r 'ok #f) (pair? a))
               (let ((path (format "~a" (assoc-ref a "path" ""))))
                 (set! plan-language-edited (cons path plan-language-edited))
                 (let mark ((ss plan-spec) (i 1))
                   (when (pair? ss)
                     (when (and (member (assoc-ref (car ss) 'kind "") '("Change" "Docs"))
                                (plan-language-same-path? (assoc-ref (car ss) 'path "") path))
                       (plan-language-mark-one (plan-language-at (car ss) i) (string-append "evidence: " path " written")))
                     (mark (cdr ss) (+ i 1))))))
              (entry
               (let ((line (car entry)) (notes '()))
                 (let mark ((ss plan-spec) (i 1))
                   (when (pair? ss)
                     (let* ((st (car ss)) (kind (assoc-ref st 'kind "")) (run (assoc-ref st 'run "")))
                       (cond ((and (equal? kind "Test")
                                   (let any ((ps plan-language-edited)) (and (pair? ps) (or (plan-language-same-path? (car ps) (assoc-ref st 'path "")) (any (cdr ps)))))
                                   (plan-language-test-passed? st (list entry)))
                              (let ((new (not (member (plan-language-at st i) plan-language-marked))))
                                (plan-language-mark-one (plan-language-at st i) (string-append "evidence: " (assoc-ref st 'name "") " passed"))
                                ;; Its first pass: is it a test of the change?
                                (when new
                                  (let ((note (plan-language-test-note st)))
                                    (when note (set! notes (cons note notes)))))))
                             ((and (equal? kind "Check") (plan-language-ran? line run))
                              (plan-language-mark-one (plan-language-at st i) "evidence: the check passed"))))
                     (mark (cdr ss) (+ i 1))))
                 (when (pair? notes)
                   (fold-result (assoc-ref c0 'id)
                                (string-append (format "~a" (assoc-ref r0 'value "")) (apply string-append (reverse notes))))))))
        (walk (cdr cs) (cdr rs))))))

(define plan-language-base-run-calls run-calls)
(define (run-calls calls)
  (let ((results (plan-language-base-run-calls calls)))
    (when (and plan-language (pair? plan-spec) (= (guard (e (else 0)) hygiene-depth) 0))
      (guard (e (else (log-note (format "plan language: new files were not marked: ~a" e))))
        (plan-language-intend calls results))
      (guard (e (else (log-note (format "plan language: the round's marks were not made: ~a" e))))
        (plan-language-round calls results)))
    results))

;; The reads of a file a plan step will write (a change, test or docs step's
;; file) stay in the window until the turn edits it (hygiene.scm,
;; hygiene-keep?): on commit-bench (dsl16, 2026-10-14) 31 of the 38 reads a
;; fold sent the model back to were of such files, read while exploring,
;; folded by age, and read again to edit them; holding the change steps'
;; files only (dsl17m) left the test and docs files' re-reads as they were.
;; A step that names a directory holds nothing: its files are many. Once
;; edited, the file's reads fold as any other's.
(define (hygiene-keep? r)
  (and plan-language (pair? plan-spec)
       (let ((path (list-ref r 1))
             (strip (lambda (p) (if (string-prefix? "./" p) (substring p 2 (string-length p)) p))))
         (and (not (assoc-ref hygiene-edits path #f))
              (let any ((ss plan-spec))
                (and (pair? ss)
                     (or (and (member (assoc-ref (car ss) 'kind "") '("Change" "Test" "Docs"))
                              (not (assoc-ref (car ss) 'none #f))
                              (equal? (strip (assoc-ref (car ss) 'path "")) path))
                         (any (cdr ss)))))))))

;; New files the model writes in the repository are marked as intended for
;; the next commit (git-intend), so a tool that lists the tracked files sees
;; them: the book's catalogue generator does, and on commit-bench (dsl8
;; 107251d0, dsl13 3422faf1) a new test file or exfil scenario was missing
;; from the catalogue the model had just regenerated. Marked only when the
;; plan named them (dsl12), the scenario of 3422faf1 was missed: the model's
;; own plan described it without its file name. The new-files reminder
;; counts marked files as new (strategy.scm, git-new-files).
(define plan-language-untracked-before '()) ; the untracked files at the turn's start

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
                                      ;; A file git showed before the turn is the person's.
                                      (not (member p plan-language-untracked-before)))
                                 (cons p out)
                                 out)))))))
    (when (pair? paths)
      (let ((marked (guard (e (else '())) (git-intend paths))))
        (when (pair? marked)
          (log-note (string-append "plan language: new files marked for the next commit: " (string-join marked ", "))))))))

;; A planned test that says it fails today runs on the unchanged code when it
;; first passes (test-before), and its result says whether it fails there:
;; the model need not set the unchanged code up by hand (four calls in dsl3's
;; 556d6d12).
(define plan-language-tests-before '())  ; ((path run content) . status) of test-before runs
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
   ;; 94f5cac4 and 3422faf1 (ADR 32, amended 2026-10-16): see plan-shape in
   ;; prompts.scm. The form is the "Assumptions:" line there; a step never
   ;; carries unstated behaviour, and the test steps list a rule's cases.
   "\n(assumes \"behaviour the request does not state\" \"the existing test or code you checked it against, and what it showed\") — a step carries only what the request states; anything else goes here, and no step relies on it until an existing test was checked against it: an assumption a test contradicts is dropped"
   "\nEvery name of the request that reads as code (session-id, cache_key, output_index) appears in names or context. If you think one is already in the code under another name, keep it in names anyway: the change is checked for the request's own spelling."
   "\nSay what the code should do as behaviour, in which cases and with what result, not as code to type. Where the request or the code leaves a detail open (which helper to call, a literal's format, which of several fixes), name the options and what will decide between them inside the step, and leave the choice to whoever carries the plan out. Copy the request's own examples, names and formats word for word."
   ;; dsl19's planner read "judge requires ready" as "judge equals ready" and
   ;; dropped Enabled() from the old condition it had been given, and its
   ;; executor followed the step against its own doubts (commit-bench
   ;; c80b38ac; ADR 32, amended 2026-10-15).
   "\nWhen a step changes an existing condition, it names each part of the old condition and says whether that part stays."
   "\nFor each rule the request states, the test steps list its cases: one per clause, and for a range, an overlap or an edge one per position (inside, crossing the start, crossing the end, equal, empty)."
   "\nA test's run is the repository's own command for it, in its language (go test ./src -run TestVerboseFlag, pytest tests/test_x.py::test_y, npm test -- x.test.js)."
   "\nFor example:"
   "\n(change \"src/flags.py\" \"parse_flags\" \"accept --verbose and set verbose\")"
   "\n(test \"tests/test_flags.py\" \"test_verbose_flag\" \"--verbose sets verbose and -q does not\" (fails-today \"the flag is unknown\") (run \"pytest tests/test_flags.py -k test_verbose_flag\"))"
   "\n(docs \"README.md\" \"the flags section lists --verbose\")"
   "\n(names \"--verbose\" \"verbose\")"
   "\n(check \"pytest\")"))

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
    (cond ((not head) (string-append where " is not a list that starts with change, test, docs, check, none, names, context or assumes"))
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
          ((equal? head "assumes")
           (if (null? s) (string-append where ": assumes needs the behaviour it assumes")
               (list (cons 'kind head)
                     (cons 'list (list (if (pair? (cdr s)) (string-append (car s) " (checked against: " (cadr s) ")") (car s)))))))
          (else (string-append where " starts with " head ", which is none of change, test, docs, check, none, names, context or assumes")))))

;; A plan's text read as data: (steps names context problems assumptions), or
;; #f when it is not readable as forms at all.
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
         (let walk ((fs forms) (i 1) (steps '()) (names '()) (context '()) (problems '()) (assumptions '()))
           (if (null? fs)
               (list (reverse steps) names context (reverse problems) assumptions)
               (let ((st (plan-language-step (car fs) i)))
                 (cond ((string? st) (walk (cdr fs) (+ i 1) steps names context (cons st problems) assumptions))
                       ((equal? (assoc-ref st 'kind "") "names") (walk (cdr fs) (+ i 1) steps (append names (assoc-ref st 'list '())) context problems assumptions))
                       ((equal? (assoc-ref st 'kind "") "context") (walk (cdr fs) (+ i 1) steps names (append context (assoc-ref st 'list '())) problems assumptions))
                       ((equal? (assoc-ref st 'kind "") "assumes") (walk (cdr fs) (+ i 1) steps names context problems (append assumptions (assoc-ref st 'list '()))))
                       (else (walk (cdr fs) (+ i 1) (cons st steps) names context problems assumptions)))))))))

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
    (list (car read) (cadr read) (append (caddr read) known) (list-ref read 3) (list-ref read 4))))

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
(define (plan-language-text steps names context assumptions)
  (let walk ((ss steps) (i 1) (out '()))
    (if (null? ss)
        (string-join (append (reverse out)
                             (if (pair? names) (list (string-append "Names the change produces, as the request spells them: "
                                                                    (string-join (map (lambda (n) (string-append "\"" n "\"")) names) ", ") ".")) '())
                             (if (pair? context) (list (string-append "Names it only uses: " (string-join context ", ") ".")) '())
                             ;; One unnumbered line: a plan's steps are its numbered lines.
                             (if (pair? assumptions) (list (string-append "Assumptions, which the request does not state (check each against the existing tests before relying on it): "
                                                                          (string-join assumptions "; ") ".")) '()))
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
                 (plan-language-text (car read) (cadr read) (caddr read) (list-ref read 4))))))))

;; A plan the model writes with plan-write may be forms too.
(define plan-language-base-write plan-write-answer)
(define (plan-write-answer call args)
  (let* ((text (string-trim (format "~a" (assoc-ref args "plan" ""))))
         (read (and plan-language (plan-language-read text))))
    (if (and read (pair? (car read)) (null? (list-ref read 3)))
        (begin (set! plan-spec (car read))
               (set! plan-spec-names (cadr read))
               (set! plan-spec-context (caddr read))
               (plan-language-base-write call (list (cons "plan" (plan-language-text (car read) (cadr read) (caddr read) (list-ref read 4))))))
        (let ((answer (plan-language-base-write call args)))
          ;; A plan the model writes in prose replaces the planner's forms:
          ;; set against the old forms, its steps were said to lack evidence
          ;; they did not need (commit-bench, dsl8 107251d0: a rewritten plan
          ;; of 7 steps, told of the forms' step 8). The prose plan is kept
          ;; as prose plans are, with the generic checks.
          (when (and (pair? plan-spec) (string-prefix? "Plan recorded" (format "~a" answer)))
            (let* ((before (length plan-spec)) (kept (plan-language-reconcile)))
              (log-note (string-append "plan language: the model wrote its plan in prose; " (number->string (length kept))
                                       " of the planner's " (number->string before) " steps carry over, numbered as the new plan numbers them"))))
          answer))))

;; A step's number in the plan the model reads: the one a rewrite matched
;; it to ('at), or its place among the plan's forms.
(define (plan-language-at st i) (assoc-ref st 'at i))

;; The model may write its plan again, in its own words: on commit-bench it
;; did so in 4 of 6 attempts once it had read the code ("let me write the
;; plan and make the changes"), carrying what it had learned (a separator,
;; an existing test to change, the ADR's file). The rewrite is the plan from
;; then on; the planner's forms that it still holds keep their checks: each
;; form is matched to the new plan's step of the same kind that names its
;; file (Change, Docs), its test (Test) or its command (Check), and takes
;; that step's number; a form the new plan dropped is dropped. The names and
;; context the request gave stay.
(define (plan-language-line-kind line)
  (let ((head (if (> (string-length line) 40) (substring line 0 40) line)))
    (let find ((ks '("Change" "Test" "Docs" "Check")))
      (cond ((null? ks) #f)
            ((string-contains? head (string-append (car ks) ":")) (car ks))
            (else (find (cdr ks)))))))

(define (plan-language-basename p)
  (let ((parts (string-split p "/"))) (if (pair? parts) (car (reverse parts)) p)))

(define (plan-language-match st lines)
  (let* ((kind (assoc-ref st 'kind "")) (path (assoc-ref st 'path "")) (name (assoc-ref st 'name ""))
         (key (cond ((assoc-ref st 'none #f) "none")
                    ((equal? kind "Test") name)
                    ((equal? kind "Check") (assoc-ref st 'run ""))
                    (else (plan-language-basename path)))))
    (let find ((ls lines) (j 1) (first-of-kind #f))
      (cond ((null? ls) (and (equal? kind "Check") first-of-kind))
            ((equal? (plan-language-line-kind (car ls)) kind)
             (if (and (not (string-empty? key)) (string-contains? (car ls) key))
                 j
                 (find (cdr ls) (+ j 1) (or first-of-kind j))))
            (else (find (cdr ls) (+ j 1) first-of-kind))))))

(define (plan-language-reconcile)
  (let* ((lines (guard (e (else '())) (plan-steps)))
         (kept (let walk ((ss plan-spec) (out '()))
                 (if (null? ss)
                     (reverse out)
                     (let ((j (plan-language-match (car ss) lines)))
                       (walk (cdr ss) (if j (cons (cons (cons 'at j) (car ss)) out) out)))))))
    (set! plan-spec kept)
    (set! plan-language-marked '())
    kept))

;; Whether a command the turn ran and saw pass since the last edit (passed,
;; run-entry's) covers what: it contains it.
(define (plan-language-passed? passed what)
  (let any ((ps passed))
    (and (pair? ps)
         (or (plan-language-ran? (car (car ps)) what)
             (any (cdr ps))))))

(define (plan-language-whole? passed) (whole-check-passed? passed))

;; The plan's steps that lack their evidence, as lines, and the steps that
;; have it, as (number . note), set against the change (plan-evidence) and
;; the runs that passed since the last edit (passed, run-entry's).
;;
;; The repository's whole check passing since the last edit (whole) stands
;; for a test step's run, and for a check step's only when the step's
;; command checks the code (check-command?: a test run, a build, a vet or a
;; linter, a make target), as the whole check does. Another check, a
;; generator's --check or a browser check, passes itself: the whole check
;; need not run it. On commit-bench (dsl16, 550e3b9, c97f9a41,
;; 20261013T224535Z-ac7eaa5c) the plan's `python3
;; docs/book/source/generate.py && python3 docs/book/source/generate.py
;; --check` step was met by a make validate that ran no generator (call
;; 3217) after test edits had made the inventory stale (3211), the evidence
;; message (3248) did not name it, and the change ended with it stale (ADR
;; 32, amended 2026-10-15).
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
          (let* ((entries (reverse unmet))
                 (asked (finish-ask car (filter pair? entries)))
                 (unmet-lines (map (lambda (e) (if (pair? e) (cdr e) e))
                                   (filter (lambda (e) (or (string? e) (member e (car asked)))) entries))))
            (list (append unmet-lines
                          (if (> (cdr asked) 0)
                              (list (string-append (number->string (cdr asked))
                                                   " more of the plan's tests are not named here: say in your report which of them the request asks for"))
                              '())
                          (if (pair? missing)
                              (list (string-append "the change does not contain "
                                                   (string-join (map (lambda (n) (string-append "\"" n "\"")) missing) ", ")
                                                   ", which the plan says it produces"))
                              '())
                          (plan-language-docs-unmet))
                  (reverse met)))
          (let* ((st (car ss)) (kind (assoc-ref st 'kind "")) (path (assoc-ref st 'path "")) (name (assoc-ref st 'name ""))
                 (label (string-append "step " (number->string (plan-language-at st i)) " (" kind (if (string-empty? path) "" (string-append " " path))
                                       (if (string-empty? name) "" (string-append " " name)) ")"))
                 ;; A problem that asks for a test to be written is a pair
                 ;; (key . line): the review names a few of those, once
                 ;; (finish-ask).
                 (problem
                  (cond ((assoc-ref st 'none #f) #f)
                        ((member kind '("Change" "Docs"))
                         (and (member path unchanged) (string-append label ": the file is unchanged")))
                        ((equal? kind "Test")
                         (cond ((not (plan-language-test-present? st))
                                (cons (string-append "plan " label) (string-append label ": the test is not in that file")))
                               ((not (or (plan-language-test-passed? st passed) whole))
                                (string-append label ": it has not passed since your last edit"))
                               ((plan-language-passes-before? st)
                                (cons (string-append "plan " label)
                                      (string-append label ": it passes on the unchanged code too (`" (assoc-ref st 'run "")
                                                     "` on a copy of the last commit with your test file in it), so it does not show the change: make it fail there")))
                               (else #f)))
                        ((equal? kind "Check")
                         (let ((run (assoc-ref st 'run "")))
                           (and (not (or (plan-language-passed? passed run) (and whole (check-command? run))))
                                (string-append label " " run ": it has not passed since your last edit"))))
                        (else #f))))
            (if problem
                (walk (cdr ss) (+ i 1) (cons problem unmet) met)
                (walk (cdr ss) (+ i 1) unmet (cons (cons (plan-language-at st i) (if (assoc-ref st 'none #f) "none" (string-append "evidence: " kind " step met"))) met))))))))

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
