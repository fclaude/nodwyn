;; How the model's shell calls reach the shell and come back (ADR 32).
;; Loaded first of the working habits: it wraps run-calls innermost and
;; fold-result, so the habits after it see the model's calls as written and
;; their hints land on the plain-text rendering.
;;
;; A model used to a bash tool chains commands in most calls (`cd … &&
;; pytest … | tail -1`, `git stash && …; git stash pop`). With shell-exec's
;; cmd/args the same model sent 8–15 malformed calls per task on some
;; benchmark tasks, and on one never ran a test; results reached it as a JSON
;; string with escaped newlines. Here a command line sent through cmd/args is
;; answered, unrun, with the bash -c call that runs it; shell results read as
;; plain text; and a test run with failures counts as a run.

;; Shell results as plain text: the exit status, then the output; long
;; output keeps its end, where test runners print their summary. The raw
;; result stays JSON for the strategies that parse it; only what the model
;; reads changes, through fold-result, including folds other strategies make
;; with a hint appended after the raw value.
(define shell-head-chars 1500)
(define shell-tail-chars 6000)

(define (shell-clip s)
  (let ((n (string-length s)))
    (if (<= n (+ shell-head-chars shell-tail-chars 200))
        s
        (string-append (substring s 0 shell-head-chars)
                       (format "\n[… ~a characters omitted …]\n" (- n shell-head-chars shell-tail-chars))
                       (substring s (- n shell-tail-chars) n)))))

;; A shell-exec result's text, or #f when the text is not one.
(define (shell-plain raw)
  (let ((v (guard (e (else #f)) (json-parse raw))))
    (and (pair? v) (assoc-ref v "status" #f)
         (let ((out (format "~a" (assoc-ref v "stdout" "")))
               (err (format "~a" (assoc-ref v "stderr" ""))))
           (string-append (let ((st (assoc-ref v "status" "")))
                            (if (integer? st) (format "exit ~a" st) (format "~a" st)))
                          (if (equal? out "") "" (string-append "\n" (shell-clip out)))
                          (if (equal? err "") "" (string-append "\n[stderr]\n" (shell-clip err))))))))

;; Where needle first occurs in s, or #f.
(define (shell-find s needle)
  (let ((n (string-length s)) (k (string-length needle)))
    (let scan ((i 0))
      (cond ((> (+ i k) n) #f)
            ((equal? (substring s i (+ i k)) needle) i)
            (else (scan (+ i 1)))))))

;; Hints other strategies append start on a line of their own with "[hint:".
(define (shell-render text)
  (let* ((s (format "~a" text))
         (cut (shell-find s "\n[hint:"))
         (head (if cut (substring s 0 cut) s))
         (tail (if cut (substring s cut (string-length s)) ""))
         (plain (and (string-prefix? "{" head) (shell-plain head))))
    (if plain (string-append plain tail) s)))

(define shell-base-fold fold-result)
(define (fold-result id text) (shell-base-fold id (shell-render text)))

;; Command lines sent through cmd/args (a program with arguments in cmd,
;; shell operators in args, or args as a string) are answered, unrun, with
;; the bash -c call that runs them as meant: a strategy may run or skip a
;; model's call but not rewrite it (the core ends the turn).
(define shell-operators '("|" "||" "&&" ";" ">" ">>" "<" "&" "2>&1" "&>" "2>/dev/null" ">/dev/null" "1>/dev/null"))

(define shell-safe-chars
  "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:,+@%")

(define (shell-quote a)
  (if (and (> (string-length a) 0)
           (let safe ((i 0))
             (or (= i (string-length a))
                 (and (string-contains? shell-safe-chars (substring a i (+ i 1)))
                      (safe (+ i 1))))))
      a
      (string-append "'" (string-join (string-split a "'") "'\\''") "'")))

(define (shell-shell-line call)
  (let* ((a (guard (e (else '())) (json-parse (format "~a" (assoc-ref call 'args "{}")))))
         (cmd (if (pair? a) (format "~a" (assoc-ref a "cmd" "")) ""))
         (raw (if (pair? a) (assoc-ref a "args" '()) '()))
         (args (cond ((list? raw) (map (lambda (x) (format "~a" x)) raw))
                     ((string? raw) (let ((l (guard (e (else #f)) (json-parse raw))))
                                      (if (list? l) (map (lambda (x) (format "~a" x)) l) (list raw))))
                     (else '())))
         (string-args (string? raw)))
    (and (equal? (assoc-ref call 'name) "shell-exec")
         (not (member cmd '("bash" "sh" "/bin/bash" "/bin/sh" "/usr/bin/bash")))
         (or (string-contains? cmd " ")
             string-args
             (let any ((as args))
               (and (pair? as)
                    (or (member (car as) shell-operators)
                        (string-prefix? "2>" (car as))
                        (any (cdr as))))))
         (string-join (cons cmd (map (lambda (x) (if (or (member x shell-operators) (string-prefix? "2>" x) string-args)
                                                      x
                                                      (shell-quote x)))
                                     args))
                      " "))))

(define (shell-reason line)
  (string-append "shell-exec runs one program with its arguments, with no shell, and this is a command line. To run it as a shell would, call shell-exec with cmd \"bash\" and args [\"-c\", "
                 (json-emit line) "]."))

(define shell-base-run-calls run-calls)
(define (run-calls calls)
  (let* ((lines (map shell-shell-line calls))
         (kept (let keep ((cs calls) (ls lines))
                 (cond ((null? cs) '())
                       ((car ls) (keep (cdr cs) (cdr ls)))
                       (else (cons (car cs) (keep (cdr cs) (cdr ls)))))))
         (ran (if (null? kept) '() (shell-base-run-calls kept))))
    (let merge ((cs calls) (ls lines) (rs ran) (out '()))
      (cond ((null? cs) (reverse out))
            ((car ls)
             (let ((reason (shell-reason (car ls))))
               (skip-tools (list (car cs)) reason)
               (merge (cdr cs) (cdr ls) rs
                      (cons (list (cons 'id (assoc-ref (car cs) 'id)) (cons 'name (assoc-ref (car cs) 'name))
                                  (cons 'ok #f) (cons 'value (string-append "Not executed: " reason)))
                            out))))
            (else
             (let ((c (car cs)) (r (car rs)))
               (when (equal? (assoc-ref c 'name) "shell-exec")
                 (let ((plain (shell-plain (format "~a" (assoc-ref r 'value "")))))
                   (when plain (shell-base-fold (assoc-ref c 'id) plain))))
               (merge (cdr cs) (cdr ls) (cdr rs) (cons r out))))))))

;; A test command ran when it exited 0 or printed a runner's summary: a run
;; with failures is still a run, so the "cannot run the tests" way out is
;; offered only for commands that never got that far.
(define shell-summary-marks
  '("passed in" "failed in" "passed," "failed," "error in" "errors in" "tests finished" "tests passed" "no tests ran"))

(define (ran-ok? result)
  (and (assoc-ref result 'ok #f)
       (let ((v (guard (e (else #f)) (json-parse (format "~a" (assoc-ref result 'value ""))))))
         (and (pair? v)
              (or (equal? (assoc-ref v "status" 1) 0)
                  (let ((o (string-downcase (string-append (format "~a" (assoc-ref v "stdout" ""))
                                                           (format "~a" (assoc-ref v "stderr" ""))))))
                    (or (let any ((ms shell-summary-marks))
                          (and (pair? ms) (or (string-contains? o (car ms)) (any (cdr ms)))))
                        (and (string-contains? o "ran ") (string-contains? o " test")))))))))
