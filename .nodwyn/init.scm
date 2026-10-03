;; Loaded after the prelude when nodwyn starts in this repository (ADR 14).
;; Everything here registers or describes tools; nothing here performs an
;; action.
(load "dev/go.scm")
(load "dev/git.scm")
(load "dev/repo.scm")

;; These tools only look at the repository: rounds of nothing else count
;; toward the default loop's reading reminder (strategy.scm).
(define look-tools (append look-tools '("grep-repo" "find-files" "read-adr" "git-log")))
