;; expect: confirm
;; L1 (P4): an edit's replacement is written content, so a credential in
;; it is a security confirm, as in a whole-file write (014).
(fs-edit "notes.txt" "no secret shape" "x-api-key: sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv")
