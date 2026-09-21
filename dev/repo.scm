;; Repository navigation tools.

(register-tool "grep-repo"
  (lambda (pattern glob) (run "git" (list "grep" "-n" "-I" "-e" pattern "--" glob)))
  '((doc . "Search tracked files for a regular expression. glob limits the files, e.g. \"*.go\" or \"docs/*.md\"; use \"*\" for all. Output is file:line:text.")
    (params . (("pattern" "string" "regular expression (git grep syntax)")
               ("glob" "string" "pathspec glob, e.g. *.go")))))

(register-tool "find-files"
  (lambda (glob) (run "git" (list "ls-files" "--" glob)))
  '((doc . "List tracked files matching a glob, e.g. \"internal/*/*.go\".")
    (params . (("glob" "string" "pathspec glob")))))

;; read-adr: find docs/adr/NNNN-*.md by number and read it — Scheme
;; composition over fs-list and fs-read, no shell involved.
(define (adr-file n)
  (let ((prefix (format "~a-" (pad4 n))))
    (let loop ((names (fs-list "docs/adr")))
      (cond ((null? names) #f)
            ((string-prefix? prefix (car names)) (string-append "docs/adr/" (car names)))
            (else (loop (cdr names)))))))

(define (pad4 n)
  (let ((s (number->string n)))
    (string-append (substring "0000" 0 (- 4 (string-length s))) s)))

(register-tool "read-adr"
  (lambda (n)
    (let ((f (adr-file n)))
      (if f (fs-read f) (error "no such ADR" n))))
  '((doc . "Read architecture decision record number n from docs/adr.")
    (params . (("n" "integer" "the ADR number, e.g. 10")))))
