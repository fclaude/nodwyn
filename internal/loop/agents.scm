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
;; offered like any tool, so define it in .nodwyn/init.scm, before the tool
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

;; A sub-agent that must stop while it still asks for tools (out of rounds,
;; or repeating itself) is asked once more for its answer, so the work it did
;; is not lost with its last tool calls. It is offered the tools it had, and
;; the harness refuses any call it makes: a local server lists the tools near
;; the start of the prompt, so a request without them read the whole context
;; again. On commit-bench (dsl18m, 2026-10-14) the explorer's last request
;; re-read 25,000 to 48,000 tokens so in 16 of 18 attempts, 4-7% of a task,
;; in every build (ADR 32, amended 2026-10-14).
(define sub-agent-wrap-up-message
  "You have no tool rounds left. Answer now with what you have found: your answer or report, and anything you could not finish or check.")

;; A model may still answer with a tool call, offered tools or not (a local
;; server parses them from the model's text): such calls are skipped with
;; this reason and the model is asked again without tools, at most twice in
;; all, since the context cannot fold while a call is unanswered.
(define sub-agent-text-only-reason
  "tools are off now: write your answer as text")

;; Skip calls with reason, ask for the answer, fold: the answer. ask asks the
;; model as the sub-agent did, offering its tools; model is the name of the
;; model that is asked again without them.
(define (sub-agent-wrap-up calls reason model ask-again)
  (skip-tools calls reason)
  (add-message sub-agent-wrap-up-message)
  (let ask ((left 2))
    (let* ((reply (if (= left 2) (ask-again) (ask-model model '((tools . #f)))))
           (stray (assoc-ref reply 'tool-calls '()))
           (said (string-trim (assoc-ref reply 'content ""))))
      (if (null? stray)
          (begin (fold-context) said)
          (begin (skip-tools stray sub-agent-text-only-reason)
                 (if (and (> left 1) (string-empty? said))
                     (ask (- left 1))
                     (begin (fold-context) said)))))))

(define (call-key call)
  (string-append (format "~a" (assoc-ref call 'name "")) " " (format "~a" (assoc-ref call 'args ""))))

;; Run task in a sub-agent: a narrowed context with system as its system
;; prompt. tools lists the tool names it may use: the model is offered only
;; those, and any other call is skipped with a reason. It may instead be a
;; procedure that takes a call and says whether it may run (the model is then
;; offered every tool), or #f for all of them. An optional name labels its
;; context in the log. Returns its final answer; the context is folded, so
;; the caller's context never sees the sub-agent's work, only what the
;; caller does with the answer. The log keeps all of it. An optional options
;; alist follows the name: '((model . "NAME")) asks the configured model NAME
;; (see model-names, and labels in model-info) instead of the control model.
(define (sub-agent system task tools . rest)
  (define name (if (and (pair? rest) (car rest)) (list (car rest)) '()))
  (define options (if (and (pair? rest) (pair? (cdr rest))) (cadr rest) '()))
  (define model (assoc-ref options 'model "control"))
  (define (ask)
    (cond ((null? tools) (ask-model model '((tools . #f)))) ; no tools: offer none
          ((pair? tools) (ask-model model (list (cons 'tools tools))))
          ((equal? model "control") (ask-model))
          (else (ask-model model '()))))
  (narrow-context system (cond ((pair? name) (car name)) (agent-label agent-label) (else "sub-agent")))
  (add-message task)
  ;; A failure on the way (a model that does not answer, say) folds the
  ;; context before it reaches the caller, so a caller that goes on (delegate,
  ;; second-look) does so in its own context. The clause's test folds and is
  ;; false, so guard raises the original error again, unchanged. Each
  ;; fold-context below is its branch's last step, so nothing fails after one.
  (guard (e ((begin (guard (ignored (else #f)) (fold-context)) #f) #f))
    ;; last and runs: the latest call run and how many times in a row;
    ;; warned: a repeat was already skipped.
    (let loop ((reply (ask)) (rounds 0) (last "") (runs 0) (warned #f))
      (let ((calls (assoc-ref reply 'tool-calls '())))
        (cond
         ((null? calls)
          (fold-context)
          (string-trim (assoc-ref reply 'content "")))
         ((>= rounds sub-agent-max-rounds)
          (sub-agent-wrap-up calls "this agent is out of rounds" model ask))
         (else
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
                       (let ((said (sub-agent-wrap-up allowed "this agent was stopped for repeating the same call" model ask)))
                         (if (string-empty? said)
                             sub-agent-stopped-note
                             (string-append said "\n\n" sub-agent-stopped-note))))
                      (else
                       (unless (null? repeats) (skip-tools (reverse repeats) sub-agent-repeat-reason))
                       (unless (null? run) (run-calls (reverse run)))
                       (loop (ask) (+ rounds 1) last runs (or warned (pair? repeats)))))))))))))


;; Delegation to other models. A task's model may hand work to the models the
;; person exposed to the task when it opened (delegate-models: none by
;; default, so no tool), with the delegate tool. The model chooses by the
;; labels config.toml gives each model, since names are the user's to change.
;; The tool's description is fixed with the tool list (ADR 9), so it does not
;; list the models: a call with no task answers with the list as it is then,
;; which is how a label edited mid-task shows. A name outside the exposed set
;; is refused, whatever the model asks for. Exposing a model is the person's
;; consent that a task, and what the helper reads on the way, may reach it
;; (ADR 27); labels and exposure are never set by a project.
(define delegate-system
  "You are a helper another model has handed one self-contained task. You see only the task below, not the conversation that led to it. Do the work with the tools you have, then answer with what you did and found, and anything you could not finish or check. Your answer is all the other model reads.")

(define (delegate-join items sep)
  (if (null? items)
      ""
      (apply string-append (car items) (map (lambda (i) (string-append sep i)) (cdr items)))))

(define (delegate-describe name)
  (let ((info (model-info name)))
    (string-append "- " name ": " (assoc-ref info 'model)
                   (if (> (assoc-ref info 'num-ctx) 0)
                       (string-append ", context " (format "~a" (assoc-ref info 'num-ctx)) " tokens")
                       "")
                   (if (null? (assoc-ref info 'labels))
                       ""
                       (string-append ", labels " (delegate-join (assoc-ref info 'labels) ", "))))))

(define (delegate-listing)
  (string-append "The models you may delegate to:\n"
                 (delegate-join (map delegate-describe (delegate-models)) "\n")))

(when (pair? (delegate-models))
  (define-agent-tool "delegate"
    "Hand a self-contained task to a helper running on another model, and read its answer. The helper sees only your task, not this conversation, and has the same tools you do except this one. Call it with an empty task to list the models you may choose from and what each is labelled for; then choose by the labels."
    '(("model" "string" "the name of one of the models listed (any text when you only want the list)")
      ("task" "string" "everything the helper needs to do the task and report back; empty to list the models"))
    (lambda (call args)
      (let ((model (format "~a" (assoc-ref args "model" "")))
            (task (format "~a" (assoc-ref args "task" ""))))
        (cond ((string-empty? (string-trim task)) (delegate-listing))
              ((member model (delegate-models))
               ;; A helper is on another provider, which may be down, out of
               ;; quota or refuse the request: its failure is this call's
               ;; answer, so the task's model can go on without it.
               (guard (e (else (string-append "The helper on " model " failed, and its work is lost: "
                                              (format "~a" (assoc-ref e 'message ""))
                                              "\nDo the task another way, or hand it to another model.")))
                 (sub-agent delegate-system task
                            (filter (lambda (n) (not (equal? n "delegate"))) (tool-names))
                            "delegate" (list (cons 'model model)))))
              (else (string-append "No model " model " is available to delegate to.\n" (delegate-listing))))))))
