;; expect: deny
;; escape-link is a symlink inside the repo pointing at ../outside.txt. The
;; pin resolves links before judging, so the link does not escape.
(fs-read "escape-link")
