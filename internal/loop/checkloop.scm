;; A mechanical check loop, kept on disk (ADR 32; the rules are in the
;; system prompts, prompts.scm, and handover notes name the check). The
;; prompts already ask for a way to check the work; in
;; the benchmark's Q5 runs the model still retyped its check each time, paraphrased the
;; request's example (sympy-20916: `omega_0` for `ω0`), made only the one
;; example pass (sphinx-10466), and lost its setup after a compaction. Here
;; the check is one script in the scratch directory: the example as written,
;; the cases of the same kind around it and the existing tests that cover the
;; code, run with the project's own interpreter or build, rerun unchanged
;; after every change and extended as cases turn up. It survives when older
;; context is cleared. When the request reports something broken, the check
;; must first fail on the current code: in hrcel-1 the model wrote a thorough
;; check that still used `omega_0`, which passes before any change.
;; Two readings from the logs: when a request shows only outputs, the check
;; needs the input that gives the wrong one (sympy-20916 shows `ω0`, and the
;; model rebuilt an input that prints the right `ω₀`); and when a request
;; weighs options, its conclusion decides (django-14140: Q5 kept the old
;; behaviour behind a guard in 12 of 13 runs, where the request and the
;; hidden test called for removing it and updating the old test).
;; Reproducing the whole scenario is not always possible: sphinx-10466 shows
;; duplicates from another project's docs, and hrcel-2 spent 20 minutes on
;; seven scripts trying to rebuild them; the runs that solved it tested the
;; Catalog class directly. So when the scenario needs what the repository
;; lacks, the check calls the part that makes the wrong output.
;; A fix that widens only what the example needs (sympy-20916: letters made
;; Unicode-aware with `[^\W\d_]`, digits left as the hand-written `[0-9]`
;; instead of `\d`, in every run that reached it) breaks on the next case;
;; the same narrowness showed on 10466 and 14140.

;; A handover note names the check script and its latest result.
;; A second look when the check will not reproduce a reported problem: a
;; helper gets the request word for word and the check script, compares them
;; and suggests what may have been read differently (in hrcel-1 the model
;; swept 1755 spelled-out names and never tried the issue's own `ω0`). It
;; reads and searches, never runs or changes anything; the model tests the
;; ideas itself.
(define checkloop-request "")
(define checkloop-base-turn nodwyn-turn)
(define (nodwyn-turn request)
  (set! checkloop-request (format "~a" request))
  (checkloop-base-turn request))

(define second-look-rounds 8)

;; The helper reads the request blind first: asked with the model's check in
;; view, it adopted the check's reading (hrcel-2: "the issue asks that
;; omega_0 renders as ω₀", when the issue shows only `ω0`). So a first helper
;; states what the request shows from the request alone, working back from
;; any output it shows to the input that gives it; a second compares that
;; reading with the check.
(define second-look-read-system
  "You read a request carefully and literally, for someone who will check their own reading against yours. From the request alone, state what it shows and asks: every example exactly as written, character for character; where the request shows outputs (printed text, errors, results) rather than the inputs behind them, work out which inputs would produce each output shown, especially the wrong or unwanted one, and say what makes you think so; and the cases the request implies but does not spell out. You can read and search files inside the repository to understand how inputs become outputs, nothing outside it; you cannot run or change anything. Answer with your reading, most important first.")

(define second-look-compare-system
  "You compare two readings of a request: an independent careful reading, and someone's check script for it, which does not show the problem the request reports. List concrete differences: inputs written differently (names, symbols, characters, encodings, types, values, options), cases the reading has that the check does not try, and assumptions in the check that the request does not support. For each, say exactly what to try. You can read and search files inside the repository, nothing outside it; the check script is shown to you in full. You cannot run or change anything. Answer with the list, most likely first.")

(define second-look-tools
  (filter (lambda (t) (guard (e (else #f)) (primitive-info t) #t))
          '("fs-read" "fs-list" "index-grep" "index-search" "fs-find")))

(define (second-look-ask system task)
  (guard (e (else (string-append "(the helper failed: " (format "~a" (assoc-ref e 'message "")) ")")))
    (sub-agent system task second-look-tools "second-look" (list (cons 'rounds second-look-rounds)))))

;; The tool reads the check only where the session may read: a path the
;; repository-pin guard refuses does not raise, it ends the turn (q4-hrcel-1,
;; sympy-20916: the model kept its check in /tmp/nibb-check.sh and the task
;; stopped nine minutes in). $TMPDIR is expanded only for the model's own
;; calls, so it is expanded here.
(define (checkloop-safe-call thunk)
  (guard (e (else #f)) (thunk)))

(define (checkloop-readable-path path)
  (let* ((scratch (checkloop-safe-call scratch-dir))
         (root (checkloop-safe-call repository))
         (p (cond ((and scratch (equal? path "$TMPDIR")) scratch)
                  ((and scratch (string-prefix? "$TMPDIR/" path))
                   (string-append scratch (substring path 7 (string-length path))))
                  (else path))))
    (and (not (equal? p ""))
         (not (string-contains? p ".."))
         (or (not (string-prefix? "/" p))
             (and scratch (string-prefix? (string-append scratch "/") p))
             (and root (string-prefix? (string-append root "/") p)))
         p)))

(define-agent-tool "second-look"
  "When your check does not show the problem the request reports, get a second look: one helper reads the request on its own, without your check, and a second compares that reading with your check script and suggests what you may have read differently and what to try. They read; they do not run anything."
  '(("check" "string" "the path of your check script, such as $TMPDIR/check.sh")
    ("saw" "string" "what you ran and what it showed"))
  (lambda (call args)
    (let* ((path (format "~a" (assoc-ref args "check" "")))
           (readable (checkloop-readable-path path))
           (script (if readable
                       (guard (e (else (string-append "(could not read " path ")"))) (fs-read readable))
                       (string-append "(not shown: " path " is outside the repository and $TMPDIR; judge from what they ran and saw)")))
           (reading (second-look-ask second-look-read-system
                                     (string-append "The request, word for word:\n" checkloop-request)))
           (compared (second-look-ask second-look-compare-system
                                      (string-append "The request, word for word:\n" checkloop-request
                                                     "\n\nAn independent reading of it:\n" reading
                                                     "\n\nTheir check script (" path "):\n" script
                                                     "\n\nWhat they ran and saw:\n" (format "~a" (assoc-ref args "saw" ""))))))
      (string-append "Independent reading of the request:\n" reading "\n\nCompared with your check:\n" compared
                     (if readable "" (string-append "\n\n(Your check at " path " could not be shown to the helpers: keep it in $TMPDIR, such as $TMPDIR/check.sh, where it can be read.)"))))))

;; The model did not call second-look on its own (hrcel-1, try 3): with a
;; check that passed on the current code it went through the repository's
;; history instead. Searching the history (git log, git blame) before any
;; change to the code, tests aside, is the sign it has not reproduced the
;; problem; that result gets a hint to call second-look, a few times at most.
(define second-look-hints 2)
(define second-look-hinted 0)
(define checkloop-code-edited #f)

(define second-look-hint
  "\n[hint: searching the history before changing anything usually means the problem is not reproduced yet. If your check does not show the problem the request reports, call second-look with your check script and what it showed: a helper compares it with the request word for word and suggests what you may have read differently]")

(define (checkloop-args call)
  (let ((a (guard (e (else '())) (json-parse (format "~a" (assoc-ref call 'args "{}"))))))
    (if (pair? a) a '())))

;; A git command whose subcommand, after options such as -C DIR, is log or blame.
(define (checkloop-history-search? call)
  (and (equal? (assoc-ref call 'name) "shell-exec")
       (let* ((a (checkloop-args call))
              (cmd (format "~a" (assoc-ref a "cmd" "")))
              (argv (let ((v (assoc-ref a "args" '()))) (if (list? v) (map (lambda (x) (format "~a" x)) v) '()))))
         (and (equal? (car (reverse (string-split cmd "/"))) "git")
              (let skip ((as argv))
                (cond ((null? as) #f)
                      ((equal? (car as) "-C") (and (pair? (cdr as)) (skip (cddr as))))
                      ((string-prefix? "-" (car as)) (skip (cdr as)))
                      (else (and (member (car as) '("log" "blame")) #t))))))))

;; An edit to the code itself: a repository file that is not a test.
(define (checkloop-code-edit? call result)
  (and (member (assoc-ref call 'name) '("fs-edit" "fs-write"))
       (assoc-ref result 'ok #f)
       (let ((p (format "~a" (assoc-ref (checkloop-args call) "path" ""))))
         (not (or (string-prefix? "$TMPDIR" p) (string-prefix? "/" p) (string-contains? p "test"))))))

(define checkloop-base-run-calls run-calls)
(define (run-calls calls)
  (let ((results (checkloop-base-run-calls calls)))
    (for-each
     (lambda (c r)
       (when (checkloop-code-edit? c r) (set! checkloop-code-edited #t))
       (when (and (not checkloop-code-edited) (< second-look-hinted second-look-hints)
                  (checkloop-history-search? c))
         (set! second-look-hinted (+ second-look-hinted 1))
         (fold-result (assoc-ref c 'id) (string-append (format "~a" (assoc-ref r 'value "")) second-look-hint))))
     calls results)
    results))
