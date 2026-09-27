;; The default agent loop: first the plan gate (plan.scm), then ask the
;; model; while it asks for tools, run them (agent tools through their
;; sub-agents, agents.scm) and ask again; a reply without tool calls ends the
;; turn. Between rounds the context is compacted once it passes
;; compact-threshold (compact.scm; off by default). A request starting with
;; /optimize runs the optimizer instead (optimize.scm). A project may redefine
;; agent-turn in .nibb/init.scm (docs/scheme.md §9).
(define (agent-turn request)
  (if (optimize-request? request)
      (optimize-turn request)
      (default-agent-turn request)))

(define (default-agent-turn request)
  (plan-gate request)
  (let loop ((reply (model-turn)))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (unless (null? calls)
        (run-calls calls)
        (maybe-compact request reply)
        (loop (model-turn))))))
