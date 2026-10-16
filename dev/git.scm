;; Read-only git tools. git-status and git-diff are builtins in any git work
;; tree; this adds the history. There is deliberately no git-commit or
;; git-push: the operator commits, or in a worktree session `git-land` does
;; (ADR 34).

(register-tool "git-log"
  (lambda (n) (run "git" (list "log" "--oneline" (format "-~a" n))))
  '((doc . "Show the last n commits, one line each.")
    (params . (("n" "integer" "number of commits")))))
