;; Repository navigation tools.

;; grep-repo searches with Perl-compatible expressions (git grep -P), as
;; the models write them. It ran basic ones, where "a|b" is the three
;; characters a|b, and answered no match with "exit 1" alone: in the local
;; sessions of 2026-09-30 to 10-05 all 248 searches with an alternation came
;; back empty, 222 of which match this tree with -P, and models took the
;; silence for an answer ("the console uses no ARIA roles",
;; 20261004T043621Z-4f1e4886; a "verified by grep" claim that reached main,
;; 20261004T053047Z-1b4c67dc). Nothing found now says so, and where it did
;; not look.
;; It stays on tracked files: it runs through shell-exec, so untracked ones
;; would include files the denylist keeps from fs-read, which index-grep
;; skips (ADR 14, amended 2026-10-05).
(register-tool "grep-repo"
  (lambda (pattern glob)
    (let ((r (shell-exec "git" (list "grep" "-n" "-I" "-P" "-e" pattern "--" glob))))
      (if (eqv? (assoc-ref r "status") 1)
          (format "no match for /~a/ in ~a: tracked files only, so a file not yet added to git is not searched (index-grep searches it)" pattern glob)
          (format "exit ~a~%~a~a" (assoc-ref r "status") (assoc-ref r "stdout") (assoc-ref r "stderr")))))
  '((doc . "Search tracked files for a Perl-compatible regular expression: a|b, \\b, \\d and (?i) work, as in index-grep. glob limits the files, e.g. \"*.go\" or \"docs/*.md\"; use \"*\" for all. Output is file:line:text, or a line saying nothing matched.")
    (params . (("pattern" "string" "regular expression (Perl-compatible, git grep -P)")
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
