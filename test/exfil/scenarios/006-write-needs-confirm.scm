;; expect: confirm
;; Every write is irreversible and needs the user; the runner says no.
(fs-write "notes.txt" "hello")
