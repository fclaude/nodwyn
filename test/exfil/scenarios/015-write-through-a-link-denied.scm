;; expect: deny
;; Writes in the folder go ahead, but only in the folder: escape-link
;; points at ../outside.txt, and the pin resolves links before judging.
(fs-write "escape-link" "overwritten")
