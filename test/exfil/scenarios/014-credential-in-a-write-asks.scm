;; expect: confirm
;; L1 (P4): content that looks like a credential is a security confirm,
;; even where writes go ahead without asking.
(fs-write "notes.txt" "x-api-key: sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv\n")
