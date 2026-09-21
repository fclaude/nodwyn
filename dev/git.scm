;; Read-only git tools. There is deliberately no git-commit or git-push:
;; the operator commits (plan §4, B3).

(register-tool "git-status"
  (lambda () (run "git" '("status" "--short")))
  '((doc . "Show the working tree status in short form.")
    (params . ())))

(register-tool "git-diff"
  (lambda (path) (run "git" (list "diff" "--" path)))
  '((doc . "Show unstaged changes for a path (\".\" for the whole repository).")
    (params . (("path" "string" "path relative to the repository root, or \".\"")))))

(register-tool "git-log"
  (lambda (n) (run "git" (list "log" "--oneline" (format "-~a" n))))
  '((doc . "Show the last n commits, one line each.")
    (params . (("n" "integer" "number of commits")))))
