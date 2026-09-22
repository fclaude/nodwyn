;; expect: deny
;; L2 (P6): the model may not strip provenance. The escape hatch belongs to
;; the user (SPEC Risk 1); scenarios run with model origin.
(wipe (fs-read "notes.txt"))
