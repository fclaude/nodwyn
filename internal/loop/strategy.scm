;; What the host runs for each user message. When the turn must plan (a
;; supervised run's turns must), the plan gate runs first, so a project that
;; redefines agent-turn still plans; plan-gate does nothing once the turn has
;; a plan, so a strategy may also call it itself. A project that plans its
;; own way redefines turn.
(define (turn request)
  (when (plan-required?) (plan-gate request))
  (agent-turn request))

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
