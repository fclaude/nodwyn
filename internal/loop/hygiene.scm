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
;; Folding an old result makes the server re-read the prompt after it, so
;; reads fold in batches of at least hygiene-min-bytes; rule 1 folds results
;; near the end. The log keeps everything; fs-read ranges bring any part back.

(define hygiene-age 8)            ; rounds before a read is old
(define hygiene-stale-age 2)      ; rounds before a stale read may fold
(define hygiene-min-bytes 24000)  ; fold reads only when this much can go at once
(define hygiene-min-read 2000)    ; smaller reads stay as they are
(define hygiene-outline-lines 40) ; outline lines kept per read

;; Lines an outline keeps: definitions, across common languages.
(define hygiene-outline-words
  '("def " "async def " "class " "func " "function " "fn " "pub fn " "pub struct " "pub enum "
    "struct " "enum " "interface " "type " "impl " "module " "public " "private " "protected "
    "export " "@"))

;; Words in a test command's output that mean tests ran, pass or fail.
(define hygiene-ran-words '("passed" "failed" "ran " "tests in" "no tests ran"))

(define hygiene-depth 0)   ; sub-agents running; the rules watch the main context only
(define hygiene-round 0)   ; rounds of calls in the main context
(define hygiene-reads '()) ; unfolded reads: (id path first text round)
(define hygiene-edits '()) ; (path . round) of each file's latest edit
(define hygiene-unrun '()) ; test commands that did not run a test: (id . why)

(define hygiene-base-sub-agent sub-agent)
(define (sub-agent . args)
  (set! hygiene-depth (+ hygiene-depth 1))
  (let ((answer (guard (e (else (set! hygiene-depth (- hygiene-depth 1))
                                (error (format "~a" (assoc-ref e 'message "sub-agent failed")))))
                  (apply hygiene-base-sub-agent args))))
    (set! hygiene-depth (- hygiene-depth 1))
    answer))

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

(define (hygiene-fold-read r stale)
  (let* ((path (list-ref r 1)) (first (list-ref r 2)) (text (list-ref r 3))
         (body (hygiene-body text))
         (last (+ first (length (string-split (if (string-suffix? "\n" body) (substring body 0 (- (string-length body) 1)) body) "\n")) -1))
         (outline (hygiene-outline text)))
    (fold-result (list-ref r 0)
                 (string-append "[folded: read of " path " lines " (number->string first) "-" (number->string last)
                                (if stale ", from before you edited it (the file has changed since)" "")
                                ". The full text is in the log; read again what you need with fs-read (path, start, count)."
                                (if (null? outline) " No definitions in it.]" " Its definitions:]\n")
                                (string-join outline "\n")))))

;; A test command's output shows tests ran.
(define (hygiene-ran? result)
  (let ((out (string-downcase (format "~a" (assoc-ref result 'value "")))))
    (let any ((ws hygiene-ran-words))
      (and (pair? ws) (or (string-contains? out (car ws)) (any (cdr ws)))))))

;; Why a test command did not run: the end of its stderr, or its error.
(define (hygiene-why result)
  (let* ((v (guard (e (else #f)) (json-parse (format "~a" (assoc-ref result 'value "")))))
         (err (if (pair? v) (format "~a" (assoc-ref v "stderr" "")) (format "~a" (assoc-ref result 'error "")))))
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

;; Fold old and stale reads, once there is enough to fold at once.
(define (hygiene-sweep)
  (let* ((due (filter (lambda (r) (let ((age (- hygiene-round (list-ref r 4))))
                                    (or (>= age hygiene-age) (and (hygiene-stale? r) (>= age hygiene-stale-age)))))
                      hygiene-reads))
         (bytes (fold (lambda (n r) (+ n (string-length (list-ref r 3)))) 0 due)))
    (when (and (pair? due) (>= bytes hygiene-min-bytes))
      (for-each (lambda (r) (hygiene-fold-read r (hygiene-stale? r))) due)
      (set! hygiene-reads (filter (lambda (r) (not (member r due))) hygiene-reads))
      (log-note (string-append "hygiene: folded " (number->string (length due)) " reads, "
                               (number->string bytes) " bytes")))))

(define hygiene-base-run-calls run-calls)
(define (run-calls calls)
  (let ((results (hygiene-base-run-calls calls)))
    (when (= hygiene-depth 0)
      (set! hygiene-round (+ hygiene-round 1))
      (hygiene-note-calls calls results)
      (hygiene-sweep))
    results))
