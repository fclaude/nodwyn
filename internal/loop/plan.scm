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

;; The judge's key when a judge is configured, or #f.
(define (plan-judge)
  (and (guard (e (else #f)) (primitive-info "jev-noul") #t)
       (credential "jev-key")))

;; A plan from the control model, asked in a context of its own that is then
;; folded away: the conversation keeps only the plan the gate records. Tools
;; are offered as in every request, so a model that calls them instead of
;; writing a plan has them answered as not run and is asked once more.
(define (plan-ask prompt)
  (narrow-context (plan-system-prompt (repository)) "planner")
  (add-message prompt)
  (let ask ((tries 1))
    (let* ((reply (model-turn))
           (calls (assoc-ref reply 'tool-calls '()))
           (text (assoc-ref reply 'content "")))
      (skip-tools calls "Plan only: reply with the plan as text; the work comes after it.")
      (if (and (pair? calls) (string-empty? (string-trim text)) (< tries 2))
          (ask (+ tries 1))
          (begin (fold-context) text)))))

;; The judge's normalised score (0 to 1) for plan against request, or the
;; error it failed with.
(define (plan-score judge request plan)
  (guard (e (else e))
    (assoc-ref (jev-score judge (string-append "Request:\n" request "\n\nPlan:\n" plan)
                          plan-score-rubric plan-score-levels)
               "score")))

;; Drafts a plan and, with a judge, revises it until it scores
;; plan-accept-score or the revisions run out; a judge failure keeps the best
;; plan so far. Records the best plan under decision, and returns it.
(define (plan-best judge request decision)
  (let loop ((plan (plan-ask (plan-draft-prompt request)))
             (round 0) (best #f) (best-score 0) (revisions 0))
    (let* ((score (if judge (plan-score judge request plan) #f))
           (scored (number? score))
           (better (or (not best) (and scored (> score best-score))))
           (best (if better plan best))
           (best-score (if (and better scored) score best-score)))
      (if (or (not scored) (>= score plan-accept-score) (= round plan-max-revisions))
          (begin
            (record-plan
             (list (cons 'decision decision) (cons 'plan best) (cons 'score best-score)
                   (cons 'revisions revisions)
                   (cons 'reason (if (or scored (not judge)) ""
                                     (string-append "jev-score failed at round " (number->string round)
                                                    ": " (assoc-ref score 'message ""))))))
            best)
          (loop (plan-ask (plan-revise-prompt request plan score))
                (+ round 1) best best-score (+ revisions 1))))))

(define (plan-gate request)
  (let ((judge (plan-judge)))
    (cond ((plan-required?)
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
