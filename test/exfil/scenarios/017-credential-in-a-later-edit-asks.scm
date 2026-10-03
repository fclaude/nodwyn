;; expect: confirm
;; A call with several edits (ADR 38) is judged as a single edit is: each
;; edit's new is written content, so a credential in the second one is the
;; same security confirm as in 016, though the first edit is harmless.
(fs-edit "notes.txt" "" "" #f
         '((("old" . "ordinary") ("new" . "plain"))
           (("old" . "no secret shape") ("new" . "x-api-key: sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv"))))
