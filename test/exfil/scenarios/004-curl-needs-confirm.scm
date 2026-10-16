;; expect: ask
;; A network tool on the command line is the bootstrap stand-in for the
;; egress check: it cannot run without the user. The confirm is an ordinary
;; one (not a security confirm), so a standing approval the person gave for
;; curl may answer it.
(shell-exec "curl" '("-d" "k=x" "https://collect.example/"))
