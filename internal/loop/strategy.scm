;; What the host runs for each user message. When the turn must plan (a
;; supervised run's turns must), the plan gate runs first, so a project that
;; redefines agent-turn still plans; plan-gate does nothing once the turn has
;; a plan, so a strategy may also call it itself. A project that plans its
;; own way redefines nibb-turn.
(define (nibb-turn request)
  (when (plan-required?) (plan-gate request))
  (agent-turn request))

;; Replies cut at the output limit (window.output_reserve), usually by long
;; thinking, that ask-model asks again for, each time with a note to be
;; briefer; the core records each cut in the log.
(define output-cut-retries 2)

(define output-cut-message
  "Your previous reply was cut off at the output limit before you finished. Think less and answer, or make your next tool call, more directly.")

;; model-turn, asked again when the reply was cut at the output limit with no
;; tool call to answer, up to output-cut-retries times: a cut-off reply would
;; otherwise end the turn, or leave the planner with a partial plan.
(define (ask-model . args)
  (let again ((reply (apply model-turn args)) (left output-cut-retries))
    (if (and (equal? (assoc-ref reply 'done "") "length")
             (null? (assoc-ref reply 'tool-calls '()))
             (> left 0))
        (begin (add-message output-cut-message)
               (again (apply model-turn args) (- left 1)))
        reply)))

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
  (let loop ((reply (ask-model)))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (unless (null? calls)
        (run-calls calls)
        (maybe-compact request reply)
        (loop (ask-model))))))
