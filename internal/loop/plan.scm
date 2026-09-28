;; The plan gate (SPEC "plan trigger", ADR 19 Seam A), as user code. Before a
;; turn's work the judge says whether the request needs a plan; if it does,
;; the control model drafts one in a context of its own, the judge scores it
;; against the request, and the model revises it at most plan-max-revisions
;; times. The outcome is recorded (record-plan), and the window shows the plan
;; to the model for the rest of the turn.
;;
;; Without a judge there is no gate, unless the turn must plan: a run's turns
;; do ((plan-required?)). Then the model always drafts a plan, scored if a
;; judge is there, and the core refuses anything irreversible until a plan is
;; recorded.
;;
;; A project may redefine any of these, or plan-gate itself.

(define plan-needed-threshold 0.5) ; the judge's "needs a plan" probability at which a request gets one
(define plan-accept-score 0.7)     ; the normalised score at which a plan is accepted
(define plan-max-revisions 3)      ; revisions after the first draft (SPEC: at most 3)
(define plan-empty-retries 2)      ; more times the planner is asked when it gives no plan
(define plan-explore-rounds 12)    ; the explorer's tool rounds before a plan is drafted; 0 skips it

;; What a read-only look at the repository finds for request, from a
;; sub-agent labelled "explorer" (agents.scm), so the plan is not written
;; blind; "" when exploring is off or finds nothing. Its reads happen before
;; any plan is recorded, which the core allows: none is irreversible.
(define (plan-explore request)
  (if (<= plan-explore-rounds 0)
      ""
      (let ((rounds sub-agent-max-rounds))
        (set! sub-agent-max-rounds plan-explore-rounds)
        (let ((found (guard (e (else (set! sub-agent-max-rounds rounds) ""))
                       (sub-agent plan-explore-system (plan-explore-task request)
                                  '("fs-read" "fs-list" "index-grep") "explorer"))))
          (set! sub-agent-max-rounds rounds)
          found))))

;; The judge's key when a judge is configured, or #f.
(define (plan-judge)
  (and (guard (e (else #f)) (primitive-info "jev-noul") #t)
       (credential "jev-key")))

;; A plan from the control model, asked in a context of its own that is then
;; folded away: the conversation keeps only the plan the gate records. The
;; planner is offered no tools, so it answers in text. A model sometimes
;; answers with thinking only; it is then asked again, in a fresh context, up
;; to plan-empty-retries more times, each noted in the log.
(define (plan-ask prompt)
  (let ask ((attempt 0))
    (narrow-context (plan-system-prompt (repository)) "planner")
    (add-message prompt)
    (let* ((reply (ask-model "control" '((tools . #f))))
           (text (assoc-ref reply 'content "")))
      (skip-tools (assoc-ref reply 'tool-calls '())
                  "Plan only: reply with the plan as text; the work comes after it.")
      (fold-context)
      (if (and (string-empty? (string-trim text)) (< attempt plan-empty-retries))
          (begin
            (log-note (string-append "planner returned an empty plan, asking again ("
                                     (number->string (+ attempt 1)) "/"
                                     (number->string plan-empty-retries) ")"))
            (ask (+ attempt 1)))
          text))))

;; The judge's normalised score (0 to 1) for plan against request, or the
;; error it failed with.
(define (plan-score judge request plan)
  (guard (e (else e))
    (assoc-ref (jev-score judge (string-append "Request:\n" request "\n\nPlan:\n" plan)
                          plan-score-rubric plan-score-levels)
               "score")))

;; Drafts a plan and, with a judge, revises it until it scores
;; plan-accept-score or the revisions run out. A judge failure, or a
;; revision that comes back empty, keeps the best plan so far. Records the
;; best plan under decision, and returns it.
(define (plan-best judge request decision)
  (define (keep best best-score revisions reason)
    (record-plan (list (cons 'decision decision) (cons 'plan best) (cons 'score best-score)
                       (cons 'revisions revisions) (cons 'reason reason)))
    best)
  (let loop ((plan (plan-ask (plan-draft-with-findings request (plan-explore request))))
             (round 0) (best #f) (best-score 0) (revisions 0))
    (let* ((score (if judge (plan-score judge request plan) #f))
           (scored (number? score))
           (better (or (not best) (and scored (> score best-score))))
           (best (if better plan best))
           (best-score (if (and better scored) score best-score)))
      (cond ((and judge (not scored))
             (keep best best-score revisions
                   (string-append "jev-score failed at round " (number->string round)
                                  ": " (assoc-ref score 'message ""))))
            ((or (not scored) (>= score plan-accept-score) (= round plan-max-revisions))
             (keep best best-score revisions ""))
            (else
             (let ((revised (plan-ask (plan-revise-prompt request plan score))))
               (if (string-empty? (string-trim revised))
                   (keep best best-score revisions "")
                   (loop revised (+ round 1) best best-score (+ revisions 1)))))))))

(define (plan-gate request)
  (let ((judge (plan-judge)))
    (cond ((plan-recorded?) #f) ; this turn has its plan already
          ((plan-required?)
           (let ((plan (plan-best judge request (if judge "plan" "required"))))
             (when (string-empty? (string-trim plan))
               (error "required plan: the planner returned an empty plan"))))
          ((not judge) #f)
          (else
           (let ((p (guard (e (else e)) (jev-noul judge request plan-needed-question))))
             (cond ((not (number? p)) ; the judge failed: plan anyway, unscored
                    (record-plan (list (cons 'decision "degraded")
                                       (cons 'plan (plan-ask (plan-draft-prompt request)))
                                       (cons 'reason (string-append "jev-noul failed: " (assoc-ref p 'message ""))))))
                   ((< p plan-needed-threshold)
                    (record-plan (list (cons 'decision "proceed") (cons 'score p))))
                   (else (plan-best judge request "plan"))))))))
