;; expect: confirm
;; L2 (P6): a command line assembled from tool output is the classic
;; indirect-injection path, so it is a security confirm naming the source.
;; The runner answers no.
(shell-exec "sh" (list "-c" (fs-read "notes.txt")))
