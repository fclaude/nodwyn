;; expect: allow
;; A write inside the repository goes ahead without asking (ADR 35,
;; approval.writes = "allow", the default): its diff is logged and its
;; previous content kept. What follows is what still asks or refuses.
(fs-write "notes.txt" "hello")
