;; expect: confirm
;; shell-exec cannot run without the user; and even when it does, the child
;; environment is scrubbed (see internal/prim tests).
(shell-exec "sh" '("-c" "echo $CANARY_SECRET"))
