;; Sub-agents, written as user code over the loop's primitives
;; (docs/scheme.md §9.3). A sub-agent is a model loop in a narrowed context
;; of its own; when it answers, the context folds away and only the answer
;; remains for whoever asked. An agent tool is a tool the model calls whose
;; answer comes from the strategy, typically from a sub-agent, instead of a
;; tool body.

;; Agent tools, by name: the handler gets the call and its parsed arguments
;; and returns the text the model reads.
(define agent-tools '())

;; Register a tool the model can call, answered by (handler call args). It is
;; offered like any tool, so define it in .nibb/init.scm, before the tool
;; list is fixed. The body the guard would run only says who answers.
(define (define-agent-tool name doc params handler)
  (register-tool name
                 (lambda args (error (string-append name " is answered by the strategy's run-calls")))
                 (list (cons 'doc doc) (cons 'params params) (cons 'variadic #t)))
  (set! agent-tools (cons (cons name handler) agent-tools)))

(define (agent-tool call) (assoc-ref agent-tools (assoc-ref call 'name) #f))

;; Run a model-turn's calls: agent tools through their handlers, answered
;; with answer-call, the rest with run-tools. Returns one result alist per
;; call, as run-tools does. Without agent tools it is run-tools.
(define (run-calls calls)
  (if (null? (filter agent-tool calls))
      (run-tools calls)
      (let loop ((cs calls) (out '()))
        (if (null? cs)
            (reverse out)
            (let* ((c (car cs)) (handler (agent-tool c)))
              (if handler
                  (let ((text (handler c (json-parse (assoc-ref c 'args "{}")))))
                    (answer-call c text)
                    (loop (cdr cs) (cons (list (cons 'id (assoc-ref c 'id)) (cons 'name (assoc-ref c 'name))
                                               (cons 'ok #t) (cons 'value text))
                                         out)))
                  (loop (cdr cs) (append (reverse (run-tools (list c))) out))))))))

;; Replies a sub-agent may make with tool calls before it must answer.
(define sub-agent-max-rounds 40)

;; Run task in a sub-agent: a narrowed context with system as its system
;; prompt. tools lists the tool names it may use (the others are skipped
;; with a reason), or #f for all of them. Returns its final answer; the
;; context is folded, so the caller's context never sees the sub-agent's
;; work, only what the caller does with the answer.
(define (sub-agent system task tools)
  (narrow-context system)
  (add-message task)
  (let loop ((reply (model-turn)) (rounds 0))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (if (or (null? calls) (>= rounds sub-agent-max-rounds))
          (begin (unless (null? calls)
                   (skip-tools calls "this agent is out of rounds"))
                 (fold-context)
                 (string-trim (assoc-ref reply 'content "")))
          (let ((allowed (if tools (filter (lambda (c) (member (assoc-ref c 'name) tools)) calls) calls)))
            (unless (= (length allowed) (length calls))
              (skip-tools (filter (lambda (c) (not (member c allowed))) calls)
                          "not available to this agent"))
            (unless (null? allowed) (run-calls allowed))
            (loop (model-turn) (+ rounds 1)))))))
