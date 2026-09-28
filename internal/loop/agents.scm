;; Sub-agents, written as user code over the loop's primitives
;; (docs/scheme.md §9.3). A sub-agent is a model loop in a narrowed context
;; of its own; when it answers, the context folds away and only the answer
;; remains for whoever asked. An agent tool is a tool the model calls whose
;; answer comes from the strategy, typically from a sub-agent, instead of a
;; tool body.

;; Agent tools, by name: the handler gets the call and its parsed arguments
;; and returns the text the model reads.
(define agent-tools '())

;; The name a sub-agent's context gets in the log: the agent tool being
;; answered while run-calls runs its handler, else "sub-agent".
(define agent-label #f)

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
                  (let ((text (begin (set! agent-label (assoc-ref c 'name))
                                     (let ((t (handler c (json-parse (assoc-ref c 'args "{}")))))
                                       (set! agent-label #f)
                                       t))))
                    (answer-call c text)
                    (loop (cdr cs) (cons (list (cons 'id (assoc-ref c 'id)) (cons 'name (assoc-ref c 'name))
                                               (cons 'ok #t) (cons 'value text))
                                         out)))
                  (loop (cdr cs) (append (reverse (run-tools (list c))) out))))))))

;; Replies a sub-agent may make with tool calls before it must answer.
(define sub-agent-max-rounds 40)

;; Identical calls (same tool and arguments) a sub-agent may run in a row.
;; The core stops the whole turn when a call repeats four times in a row
;; (a progress stop, which no strategy can catch), so a sub-agent that loops
;; would end its caller's work too. Instead, a further identical call is
;; skipped with sub-agent-repeat-reason, and the sub-agent is ended, with
;; what it has, if it keeps repeating after that.
(define sub-agent-repeat-limit 2)

(define sub-agent-repeat-reason
  "you already ran this exact call, and its result is above; use it, or answer with what you have")

(define sub-agent-stopped-note
  "[This agent was stopped for repeating the same call; its answer may be incomplete.]")

(define (call-key call)
  (string-append (format "~a" (assoc-ref call 'name "")) " " (format "~a" (assoc-ref call 'args ""))))

;; Run task in a sub-agent: a narrowed context with system as its system
;; prompt. tools lists the tool names it may use (the others are skipped
;; with a reason), is a procedure that takes a call and says whether it may
;; run, or is #f for all of them; an optional name labels its
;; context in the log. Returns its final answer; the context is folded, so
;; the caller's context never sees the sub-agent's work, only what the
;; caller does with the answer. The log keeps all of it.
(define (sub-agent system task tools . name)
  (narrow-context system (cond ((pair? name) (car name)) (agent-label agent-label) (else "sub-agent")))
  (add-message task)
  ;; last and runs: the latest call run and how many times in a row;
  ;; warned: a repeat was already skipped.
  (let loop ((reply (ask-model)) (rounds 0) (last "") (runs 0) (warned #f))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (if (or (null? calls) (>= rounds sub-agent-max-rounds))
          (begin (unless (null? calls)
                   (skip-tools calls "this agent is out of rounds"))
                 (fold-context)
                 (string-trim (assoc-ref reply 'content "")))
          (let ((allowed (cond ((procedure? tools) (filter tools calls))
                               (tools (filter (lambda (c) (member (assoc-ref c 'name) tools)) calls))
                               (else calls))))
            (unless (= (length allowed) (length calls))
              (skip-tools (filter (lambda (c) (not (member c allowed))) calls)
                          "not available to this agent"))
            ;; Split the allowed calls, in order, into those to run and the
            ;; repeats past the limit.
            (let walk ((cs allowed) (run '()) (repeats '()) (last last) (runs runs))
              (cond ((pair? cs)
                     (let ((key (call-key (car cs))))
                       (cond ((not (equal? key last)) (walk (cdr cs) (cons (car cs) run) repeats key 1))
                             ((< runs sub-agent-repeat-limit) (walk (cdr cs) (cons (car cs) run) repeats key (+ runs 1)))
                             (else (walk (cdr cs) run (cons (car cs) repeats) last runs)))))
                    ((and (pair? repeats) warned)
                     (skip-tools allowed "this agent was stopped for repeating the same call")
                     (fold-context)
                     (let ((said (string-trim (assoc-ref reply 'content ""))))
                       (if (string-empty? said)
                           sub-agent-stopped-note
                           (string-append said "\n\n" sub-agent-stopped-note))))
                    (else
                     (unless (null? repeats) (skip-tools (reverse repeats) sub-agent-repeat-reason))
                     (unless (null? run) (run-calls (reverse run)))
                     (loop (ask-model) (+ rounds 1) last runs (or warned (pair? repeats)))))))))))
