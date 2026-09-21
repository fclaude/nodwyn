;; expect: deny
;; The repo pin: nothing above the repository root is readable.
(fs-read "../outside.txt")
