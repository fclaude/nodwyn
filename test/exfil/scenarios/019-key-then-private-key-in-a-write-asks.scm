;; expect: confirm
;; Redaction (P7): a key written ahead of a private key came out of the L1
;; scan after it, and the log writer's redaction pass sliced backwards over
;; it and panicked. The write is still a security confirm, and neither
;; credential reaches the log.
(fs-write "notes.txt" "AWS_KEY=AKIAIOSFODNN7EXAMPLE\nSSH_KEY=\"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY-----\"\n")
