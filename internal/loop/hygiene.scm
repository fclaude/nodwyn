;; Context hygiene: keep outcomes and the passages that matter, fold the rest
;; (ADR 32). On the benchmark's dev set about 70% of the tool
;; output in the main context was test runs and file reads: attempts to find
;; how tests run, and reads of code that turned out not to matter (43% of
;; read bytes were of files never edited). Three rules, main context only:
;;   1. test commands that never got to run a test (an import error, the
;;      wrong runner) are folded to one line each once a later one runs;
;;   2. a read of a file edited since is stale: it is folded to its outline,
;;      noting the file changed;
;;   3. a read hygiene-age rounds old is folded to its outline.
;; Rules 2 and 3 leave the reads of a file being worked on, one edited in
;; the last hygiene-editing-rounds rounds: folding them sent the model back
;; to read the file it was editing (transcript.js 49 times in session
;; 20261005T004207Z-f0ddf504, "old text not found" clustering after the
;; folds), and fs-edit's answer now shows the lines it changed.
;; Folding an old result makes a server with a prompt cache read all of the
;; prompt after it again at the full input price, where a cached token costs
;; a fraction of that (a fiftieth on meta/muse-spark-1.3): a fold that takes
;; out less than it makes the server read again rarely pays for itself. So
;; reads fold in batches of at least hygiene-min-bytes, and only when the
;; batch is at least as large as the rest of the window after it, when the
;; provider's cache is not serving the context anyway, or when the context
;; is past hygiene-pressure of the high water, where room matters more.
;; Rule 1 folds results near the end. The log keeps everything; fs-read
;; ranges bring any part back.

(define hygiene-age 8)            ; rounds before a read is old
(define hygiene-stale-age 2)      ; rounds before a stale read may fold
(define hygiene-editing-rounds 4) ; a file edited this many rounds ago or less is being worked on
(define hygiene-min-bytes 24000)  ; fold reads only when this much can go at once
(define hygiene-min-read 2000)    ; smaller reads stay as they are
(define hygiene-outline-lines 40) ; outline lines kept per read
(define hygiene-cache-share 0.25) ; the cache serves the context when it read this much of the recent prompts
(define hygiene-cache-requests 4) ; the recent requests that say whether it does
(define hygiene-pressure 0.5)     ; past this share of the high water, reads fold whatever the cache

;; Lines an outline keeps: definitions, across common languages.
(define hygiene-outline-words
  '("def " "async def " "class " "func " "function " "fn " "pub fn " "pub struct " "pub enum "
    "struct " "enum " "interface " "type " "impl " "module " "public " "private " "protected "
    "export " "@"))

;; Words in a test command's output that mean tests ran, pass or fail,
;; lower-cased: pytest's and unittest's, then Go's ("ok  <tab>pkg", "--- PASS",
;; "--- FAIL", "=== RUN"), then mocha's, jest's, node's and cargo's. Go's were
;; missing until 2026-10-05: 138 of the 151 results folded as "did not run
;; any test" in the local session logs were go test runs that ran,
;; failing-before evidence among them.
(define hygiene-ran-words
  '("passed" "failed" "ran " "tests in" "no tests ran"
    "ok  \t" "--- pass" "--- fail" "--- skip" "=== run"
    "passing" "failing" "tests:" "# pass" "# fail" "test result:"))

(define hygiene-depth 0)   ; sub-agents running; the rules watch the main context only
(define hygiene-round 0)   ; rounds of calls in the main context
(define hygiene-reads '()) ; unfolded reads: (id path first text round)
(define hygiene-edits '()) ; (path . round) of each file's latest edit
(define hygiene-unrun '()) ; test commands that did not run a test: (id . why)
(define hygiene-cache '()) ; recent requests that reported the provider's cache: (prompt . cached)

;; A turn starts in the main context, with no sub-agent running. A stop
;; inside one (the person's, a limit, a progress stop) passes through guard
;; and skips the decrement below, which left the rules off for the session.
(define hygiene-base-nodwyn-turn nodwyn-turn)
(define (nodwyn-turn request)
  (set! hygiene-depth 0)
  (hygiene-base-nodwyn-turn request))

(define hygiene-base-sub-agent sub-agent)
(define (sub-agent . args)
  (set! hygiene-depth (+ hygiene-depth 1))
  (let ((answer (guard (e (else (set! hygiene-depth (- hygiene-depth 1))
                                (error (format "~a" (assoc-ref e 'message "sub-agent failed")))))
                  (apply hygiene-base-sub-agent args))))
    (set! hygiene-depth (- hygiene-depth 1))
    answer))

(define hygiene-base-model-turn model-turn)
(define (model-turn . args)
  (let ((reply (apply hygiene-base-model-turn args)))
    (when (= hygiene-depth 0)
      (let ((prompt (assoc-ref reply 'prompt-tokens 0)) (cached (assoc-ref reply 'cached-tokens #f)))
        (when (and (integer? cached) (integer? prompt) (> prompt 0))
          (set! hygiene-cache (hygiene-take (cons (cons prompt cached) hygiene-cache) hygiene-cache-requests)))))
    reply))

(define (hygiene-args call)
  (let ((a (guard (e (else '())) (json-parse (format "~a" (assoc-ref call 'args "{}"))))))
    (if (pair? a) a '())))

(define (hygiene-path p)
  (let ((s (format "~a" p))) (if (string-prefix? "./" s) (substring s 2 (string-length s)) s)))

(define (hygiene-take l n) (if (or (null? l) (= n 0)) '() (cons (car l) (hygiene-take (cdr l) (- n 1)))))

(define (hygiene-cut s n) (if (> (string-length s) n) (string-append (substring s 0 n) "...") s))

;; A ranged read starts "[lines A-B of N]": its first line number and body.
(define (hygiene-first-line text)
  (if (string-prefix? "[lines " text)
      (or (string->number (car (string-split (substring text 7 (string-length text)) "-"))) 1)
      1))
(define (hygiene-body text)
  (if (string-prefix? "[lines " text)
      (let ((lines (string-split text "\n"))) (string-join (if (pair? lines) (cdr lines) '()) "\n"))
      text))

(define (hygiene-outline text)
  (let walk ((lines (string-split (hygiene-body text) "\n")) (n (hygiene-first-line text)) (out '()))
    (cond ((or (null? lines) (>= (length out) hygiene-outline-lines)) (reverse out))
          ((let ((t (string-trim (car lines))))
             (let any ((ws hygiene-outline-words))
               (and (pair? ws) (or (string-prefix? (car ws) t) (any (cdr ws))))))
           (walk (cdr lines) (+ n 1) (cons (string-append "  " (number->string n) ": " (hygiene-cut (string-trim (car lines)) 120)) out)))
          (else (walk (cdr lines) (+ n 1) out)))))

(define (hygiene-fold-text r stale)
  (let* ((path (list-ref r 1)) (first (list-ref r 2)) (text (list-ref r 3))
         (body (hygiene-body text))
         (last (+ first (length (string-split (if (string-suffix? "\n" body) (substring body 0 (- (string-length body) 1)) body) "\n")) -1))
         (outline (hygiene-outline text)))
    (string-append "[folded: read of " path " lines " (number->string first) "-" (number->string last)
                   (if stale ", from before you edited it (the file has changed since)" "")
                   ". The full text is in the log; read again what you need with fs-read (path, start, count)."
                   (if (null? outline) " No definitions in it.]" " Its definitions:]\n")
                   (string-join outline "\n"))))

(define (hygiene-fold-read r stale) (fold-result (list-ref r 0) (hygiene-fold-text r stale)))

;; What go test says of a package that ran no test: it did not build, or
;; its test binary did not start. Its "failed" is not pytest's.
(define hygiene-unran-words '("[build failed]" "[setup failed]"))

;; A test command ran tests: it exited 0, since only a command that failed
;; may have failed before running one, or its output says tests ran.
(define (hygiene-ran? result)
  (or (ran-ok? result)
      (let ((out (fold (lambda (s w) (string-replace s w ""))
                       (string-downcase (format "~a" (assoc-ref result 'value "")))
                       hygiene-unran-words)))
        (let any ((ws hygiene-ran-words))
          (and (pair? ws) (or (string-contains? out (car ws)) (any (cdr ws))))))))

;; Why a test command did not run: the end of its stderr, else of its
;; output (go test reports a package that does not build there), or its
;; error.
(define (hygiene-why result)
  (let* ((v (guard (e (else #f)) (json-parse (format "~a" (assoc-ref result 'value "")))))
         (err (if (pair? v)
                  (let ((e (string-trim (format "~a" (assoc-ref v "stderr" "")))))
                    (if (string-empty? e) (format "~a" (assoc-ref v "stdout" "")) e))
                  (format "~a" (assoc-ref result 'error "")))))
    (let ((t (string-trim err)))
      (hygiene-cut (if (> (string-length t) 200) (substring t (- (string-length t) 200) (string-length t)) t) 200))))

(define (hygiene-note-calls calls results)
  (let walk ((cs calls) (rs results))
    (when (and (pair? cs) (pair? rs))
      (let* ((c (car cs)) (r (car rs)) (name (assoc-ref c 'name "")) (ok (assoc-ref r 'ok #f)))
        (cond ((and ok (equal? name "fs-read"))
               (let ((text (format "~a" (assoc-ref r 'value ""))))
                 (when (>= (string-length text) hygiene-min-read)
                   (set! hygiene-reads
                         (cons (list (assoc-ref c 'id) (hygiene-path (assoc-ref (hygiene-args c) "path" "")) (hygiene-first-line text) text hygiene-round)
                               hygiene-reads)))))
              ((and ok (edit-call? c))
               (set! hygiene-edits (cons (cons (hygiene-path (assoc-ref (hygiene-args c) "path" "")) hygiene-round) hygiene-edits)))
              ((test-call? c)
               (if (hygiene-ran? r)
                   (begin
                     (for-each (lambda (u)
                                 (fold-result (car u) (string-append "[folded: this test command did not run any test (" (cdr u) "); a later one did: " (hygiene-cut (command-line c) 200) "]")))
                               hygiene-unrun)
                     (set! hygiene-unrun '()))
                   (set! hygiene-unrun (cons (cons (assoc-ref c 'id) (hygiene-why r)) hygiene-unrun))))))
      (walk (cdr cs) (cdr rs)))))

(define (hygiene-stale? r)
  (let ((e (assoc-ref hygiene-edits (list-ref r 1) #f)))
    (and e (> e (list-ref r 4)))))

;; A read of a file edited in the last hygiene-editing-rounds rounds: the
;; file is being worked on, and its reads stay. hygiene-edits holds the
;; latest edit of a file first.
(define (hygiene-editing? r)
  (let ((e (assoc-ref hygiene-edits (list-ref r 1) #f)))
    (and e (< (- hygiene-round e) hygiene-editing-rounds))))

(define (hygiene-tokens s) (quotient (+ (string-length s) 2) 3)) ; the window's estimate

;; Whether the provider's prompt cache serves this context: it read at least
;; hygiene-cache-share of the recent requests' prompts. A provider that does
;; not say (Ollama) is taken to; ADR 9's window is built for its cache.
(define (hygiene-cache-serves?)
  (or (null? hygiene-cache)
      (>= (fold (lambda (n r) (+ n (cdr r))) 0 hygiene-cache)
          (* hygiene-cache-share (fold (lambda (n r) (+ n (car r))) 0 hygiene-cache)))))

;; Tokens folding reads takes out of the window: what each shows there,
;; shortened past max-result-tokens as the window shortens it, less its
;; outline.
(define (hygiene-saved due)
  (let ((cap (assoc-ref (window-limits) 'max-result-tokens 0)))
    (fold (lambda (n r)
            (let ((shown (hygiene-tokens (list-ref r 3))))
              (+ n (max 0 (- (if (and (> cap 0) (> shown cap)) cap shown)
                             (hygiene-tokens (hygiene-fold-text r (hygiene-stale? r))))))))
          0 due)))

;; The window from the earliest of the reads the window still shows to its
;; end (reads are noted in order, so the last of them is the earliest):
;; what folding them makes the server read again. 0 when it shows none.
(define (hygiene-after due)
  (let walk ((rs (reverse due)))
    (if (null? rs)
        0
        (or (guard (e (else #f)) (context-tokens (list-ref (car rs) 0)))
            (walk (cdr rs))))))

;; Why the due reads may fold now, or #f while folding them would make the
;; server read again more than it takes out.
(define (hygiene-fold-reason due)
  (cond ((not (hygiene-cache-serves?)) "the provider's cache is not serving this context")
        ((>= (context-tokens) (* hygiene-pressure (assoc-ref (window-limits) 'high-water 0)))
         "the context is filling the window")
        (else
         (let ((after (hygiene-after due)) (saved (hygiene-saved due)))
           (cond ((= after 0) "the window no longer shows them")
                 ((>= (* 2 saved) after)
                  (string-append "they are " (number->string saved) " of the " (number->string after)
                                 " tokens the server reads again"))
                 (else #f))))))

;; Fold old and stale reads, once there is enough to fold at once and
;; folding it pays (hygiene-fold-reason).
(define (hygiene-sweep)
  (let* ((due (filter (lambda (r) (let ((age (- hygiene-round (list-ref r 4))))
                                    (and (not (hygiene-editing? r))
                                         (or (>= age hygiene-age) (and (hygiene-stale? r) (>= age hygiene-stale-age))))))
                      hygiene-reads))
         (bytes (fold (lambda (n r) (+ n (string-length (list-ref r 3)))) 0 due))
         (why (and (pair? due) (>= bytes hygiene-min-bytes) (hygiene-fold-reason due))))
    (when why
      (for-each (lambda (r) (hygiene-fold-read r (hygiene-stale? r))) due)
      (set! hygiene-reads (filter (lambda (r) (not (member r due))) hygiene-reads))
      (log-note (string-append "hygiene: folded " (number->string (length due)) " reads, "
                               (number->string bytes) " bytes: " why)))))

(define hygiene-base-run-calls run-calls)
(define (run-calls calls)
  (let ((results (hygiene-base-run-calls calls)))
    (when (= hygiene-depth 0)
      (set! hygiene-round (+ hygiene-round 1))
      (hygiene-note-calls calls results)
      (hygiene-sweep))
    results))
