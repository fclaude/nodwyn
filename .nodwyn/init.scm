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
  '((doc . "Run this repository's checks for what changed since the branch left its upstream (make validate, scripts/validate): gofmt, make accept, the browser checks for console, host and fake changes, the book catalogue's check after Go or Scheme changes (generate.py --check, counting the files git does not track yet), the release tests after a change to the release publisher, each changed Go test against the unchanged code, and the house rules a diff can show, among them removed tests and scenarios and edits to the guard. It does not build the book or the expert guide: the release flow does. It can take ten minutes or more. Returns a summary whose last line says PASS or FAIL; quote it in your answer, warnings included.")
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

;; The change's completeness. A turn that changed Go or Scheme code and is
;; about to end is told, once a session, what its change lacks of what
;; AGENTS.md asks a change here to carry: a test, a document, a decision
;; record, the regenerated source catalogue. It may add them or say why the
;; change needs none, and goes on. These are this repository's rules, so
;; they are here and hold in both profiles: the loop nodwyn ships says
;; nothing of them in the lean profile (lean-quiet), and on commit-bench,
;; this repository at each task's parent, 96 lean patches had a test 88% of
;; the time and a document 61%, against 100% and 78% under the full loop
;; (ADR 32, amended 2026-10-18). It comes after the validate reminder, last
;; before the turn ends. Nothing in it uses the rest of this file, only the
;; loop the binary carries, so commit-bench can load it over an older
;; revision's init.scm to measure it.
(define repo-finish-told #f)

;; Code is Go and Scheme outside test/, which holds the exfiltration
;; scenarios (Scheme), the browser checks and the fakes. A test is what
;; scripts/validate counts as one; a document is anything under docs/ but
;; the two files a generator writes, or Markdown anywhere.
(define (repo-finish-code? p)
  (and (not (string-prefix? "test/" p))
       (or (and (string-suffix? ".go" p) (not (string-suffix? "_test.go" p)))
           (string-suffix? ".scm" p))))

(define (repo-finish-test? p)
  (or (string-suffix? "_test.go" p) (string-prefix? "test/exfil/" p) (string-prefix? "test/browser/checks/" p)))

(define repo-finish-catalogue "docs/book/source/catalogue.tex")

(define (repo-finish-doc? p)
  (or (string-suffix? ".md" p)
      (and (string-prefix? "docs/" p) (not (member p (list repo-finish-catalogue "docs/protocol.schema.json"))))))

(define (repo-finish-adr? p) (string-prefix? "docs/adr/" p))

;; What the catalogue counts a file as (docs/book/source/generate.py): the
;; Go files and their lines, the test files' apart, and the Scheme files and
;; their lines, every one git tracks.
(define (repo-finish-class p)
  (cond ((string-suffix? "_test.go" p) 'go-test)
        ((string-suffix? ".go" p) 'go)
        ((string-suffix? ".scm" p) 'scheme)
        (else #f)))

;; The files a unified diff shows, each (path status added removed text):
;; status "A" for a new file, "D" for a deleted one, "M" otherwise; added
;; and removed count the lines of its hunks, and text is its part of the
;; diff. A file is built as a vector while its lines are read.
(define (repo-finish-diff-files diff)
  (let walk ((lines (string-split diff "\n")) (file #f) (out '()))
    (let ((out (if (and file (or (null? lines) (string-prefix? "diff --git " (car lines))))
                   (cons (repo-finish-file-entry file) out)
                   out)))
      (cond ((null? lines) (reverse out))
            ((string-prefix? "diff --git " (car lines))
             (walk (cdr lines) (repo-finish-file-start (car lines)) out))
            (file (repo-finish-file-line! file (car lines)) (walk (cdr lines) file out))
            (else (walk (cdr lines) #f out))))))

;; "diff --git a/P b/P" names P; a rename names its new path on a line of
;; its own, "rename to P".
(define (repo-finish-file-start line)
  (let* ((rest (substring line 13 (string-length line)))
         (n (string-length rest))
         (half (quotient (- n 3) 2))
         (path (and (> n 3) (= n (+ half half 3))
                    (equal? (substring rest half (+ half 3)) " b/")
                    (equal? (substring rest 0 half) (substring rest (+ half 3) n))
                    (substring rest 0 half))))
    (vector (or path rest) "M" 0 0 #f (list line))))

(define (repo-finish-file-line! file line)
  (vector-set! file 5 (cons line (vector-ref file 5)))
  (cond ((vector-ref file 4)
         (cond ((string-prefix? "+" line) (vector-set! file 2 (+ (vector-ref file 2) 1)))
               ((string-prefix? "-" line) (vector-set! file 3 (+ (vector-ref file 3) 1)))))
        ((string-prefix? "@@" line) (vector-set! file 4 #t))
        ((string-prefix? "new file mode" line) (vector-set! file 1 "A"))
        ((string-prefix? "deleted file mode" line) (vector-set! file 1 "D"))
        ((string-prefix? "rename to " line) (vector-set! file 0 (substring line 10 (string-length line))))))

(define (repo-finish-file-entry file)
  (list (vector-ref file 0) (vector-ref file 1) (vector-ref file 2) (vector-ref file 3)
        (string-join (reverse (vector-ref file 5)) "\n")))

;; The change against base: the files the diff shows, and those git does
;; not track, status "?", as git's status names them (a directory it does
;; not track is one entry, its contents unread). #f without git, or when the
;; diff was cut at the tools' output limit and cannot be read whole.
(define (repo-finish-change base)
  (guard (e (else #f))
    (let ((state (git-state))
          (diff (format "~a" (git-diff "." #f base))))
      (and state (not (string-contains? diff "[output truncated at "))
           (append (repo-finish-diff-files diff)
                   (map (lambda (l) (let ((p (substring l 3 (string-length l)))) (list p "?" 0 0 p)))
                        (filter (lambda (l) (string-prefix? "?? " l)) state)))))))

;; What the change is measured against: the commit a session's worktree
;; started from, else the one HEAD named when the turn began, so a commit
;; made in the turn moves nothing out of it.
(define (repo-finish-base head)
  (or (guard (e (else #f)) (let ((w (worktree))) (and w (assoc-ref w "base-commit" #f))))
      head
      "HEAD"))

;; Whether the catalogue's counts moved: a Go or Scheme file was added or
;; deleted, or the lines added and removed differ for the Go files, the Go
;; test files or the Scheme files.
(define (repo-finish-counts-moved? change)
  (let walk ((es change) (go 0) (go-test 0) (scheme 0))
    (if (null? es)
        (not (and (= go 0) (= go-test 0) (= scheme 0)))
        (let* ((e (car es))
               (class (repo-finish-class (car e)))
               (delta (- (list-ref e 2) (list-ref e 3))))
          (cond ((not class) (walk (cdr es) go go-test scheme))
                ((member (cadr e) '("A" "D" "?")) #t)
                ((eq? class 'go) (walk (cdr es) (+ go delta) go-test scheme))
                ((eq? class 'go-test) (walk (cdr es) go (+ go-test delta) scheme))
                (else (walk (cdr es) go go-test (+ scheme delta))))))))

(define (repo-finish-exists? path)
  (guard (e (else #f)) (fs-read path 1 1) #t))

;; What the change lacks, as symbols, or () when the turn edited no code
;; (its files as they were when it began are before) or the repository has
;; no AGENTS.md to cite. The catalogue is asked for only where there is one.
(define (repo-finish-points before now)
  (let* ((edited (map car (filter (lambda (e) (not (member e before))) now)))
         (paths (map car now))
         (has? (lambda (pred) (pair? (filter pred paths)))))
    (if (or (null? (filter repo-finish-code? edited)) (not (repo-finish-exists? "AGENTS.md")))
        '()
        (filter identity
                (list (and (not (has? repo-finish-test?)) 'test)
                      (and (not (has? repo-finish-doc?)) 'docs)
                      (and (not (has? repo-finish-adr?)) 'adr)
                      (and (not (member repo-finish-catalogue paths))
                           (repo-finish-counts-moved? now)
                           (repo-finish-exists? repo-finish-catalogue)
                           'catalogue))))))

(define repo-finish-intro
  "This turn changed Go or Scheme code, and AGENTS.md says a change carries everything that belongs with it. The change has")

(define repo-finish-lines
  '((test . "- no test: no test file changed, and AGENTS.md asks for a test that fails on the unchanged code and passes on yours.")
    (docs . "- no document: nothing under docs/ changed, and AGENTS.md puts behaviour a person sees in docs/manual.md and wherever the book or the expert guide describe it.")
    (adr . "- no decision record: nothing under docs/adr changed, and AGENTS.md puts a decision in docs/adr, as a new ADR or an amendment of the ADR it changes.")
    (catalogue . "- an old source catalogue: Go or Scheme files or their line counts changed and docs/book/source/catalogue.tex did not, and AGENTS.md asks you to regenerate it with python3 docs/book/source/generate.py.")))

(define repo-finish-close
  "Add what the change needs, or say in your answer why it needs none of it.")

(define (repo-finish-message points)
  (string-join (append (list repo-finish-intro)
                       (map (lambda (p) (assoc-ref repo-finish-lines p)) points)
                       (list repo-finish-close))
               "\n"))

;; After the message, the turn goes on as the default chat loop does.
(define (repo-finish-continue request)
  (let loop ((reply (ask-model)) (reading 0))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (unless (null? calls)
        (run-calls calls)
        (maybe-compact request reply)
        (loop (ask-model) (reading-rounds calls reading))))))

(define repo-finish-base-turn default-agent-turn)

(define (default-agent-turn request)
  (if repo-finish-told
      (repo-finish-base-turn request)
      (let* ((base (repo-finish-base (guard (e (else #f)) (git-head))))
             (before (repo-finish-change base)))
        (repo-finish-base-turn request)
        (let* ((now (and before (not turn-stopping) (repo-finish-change base)))
               (points (if now (repo-finish-points before now) '())))
          (when (pair? points)
            (set! repo-finish-told #t)
            (add-message (repo-finish-message points))
            (repo-finish-continue request))))))
