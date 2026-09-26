;; The default agent loop: ask the model; while it asks for tools, run them
;; and ask again; a reply without tool calls ends the turn. A project may
;; redefine agent-turn in .nibb/init.scm (docs/scheme.md §9).
(define (agent-turn request)
  (let loop ((reply (model-turn)))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (unless (null? calls)
        (run-tools calls)
        (loop (model-turn))))))
