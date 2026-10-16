;; expect: ask
;; shell-exec cannot run without the user, by an ordinary confirm a standing
;; approval for sh may answer; and even when it does, the child environment
;; is scrubbed (see internal/prim tests).
(shell-exec "sh" '("-c" "echo $CANARY_SECRET"))
