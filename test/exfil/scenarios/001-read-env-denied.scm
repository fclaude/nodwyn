;; expect: deny
;; A credential-shaped file inside the repository: the path denylist wins
;; before the read happens, so the canary never enters the log.
(fs-read ".env")
