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
  (plan-language-base-turn request))

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
   "\n(names \"name\" ...) — every name the request gives for something the change must produce (a header, a flag, a field, a function, a file), spelled as the request spells it"
   "\n(context \"name\" ...) — every other name the request gives: what the change only uses or reads"
   "\nEvery name the request gives appears in names or context. If you think one is already in the code under another name, keep it in names anyway: the change is checked for the request's own spelling."
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
(define (plan-form-sub form head)
  (let find ((xs (cdr form)))
    (cond ((null? xs) #f)
          ((equal? (plan-form-head (car xs)) head)
           (let ((s (plan-form-strings (car xs)))) (if (pair? s) (car s) "")))
          (else (find (cdr xs))))))

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
(define (plan-language-read text)
  (let ((forms (guard (e (else #f)) (read-data text))))
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
                             (if (pair? context) (list (string-append "Names it only uses: " (string-join context ", ") ".")) '()))
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
        (let* ((read (plan-language-read text))
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
        (plan-language-base-write call args))))

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
         (or (let w ((ws whole-check-words)) (and (pair? w) (or (string-contains? (car ps) (car w)) (w (cdr w)))))
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
                            '()))
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
                               ((not (or (plan-language-passed? passed name) (plan-language-passed? passed (assoc-ref st 'run "")) whole))
                                (string-append label ": it has not passed since your last edit"))
                               (else #f)))
                        ((equal? kind "Check")
                         (and (not (or (plan-language-passed? passed (assoc-ref st 'run "")) whole))
                              (string-append label " " (assoc-ref st 'run "") ": it has not passed since your last edit")))
                        (else #f))))
            (if problem
                (walk (cdr ss) (+ i 1) (cons problem unmet) met)
                (walk (cdr ss) (+ i 1) unmet (cons (cons i (if (assoc-ref st 'none #f) "none" (string-append "evidence: " kind " step met"))) met))))))))

;; Mark the steps that have their evidence, so the model need not.
(define (plan-language-mark met)
  (for-each (lambda (m) (guard (e (else #f)) (record-step (car m) "done" (cdr m)))) met))

(define (plan-language-unmet-message unmet)
  (string-append "Before you finish, your plan's evidence is missing:"
                 (apply string-append (map (lambda (u) (string-append "\n- " u)) unmet))
                 "\nDo what is missing, or say in your report why a step does not apply."))
