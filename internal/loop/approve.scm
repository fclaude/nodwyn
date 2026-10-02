;; Judged approval (ADR 18 §5, ADR 20 Seam C), as user code. When a session's
;; confirms are answered by the judge instead of a person (a run started with
;; --judge), the core calls (approve-call call) for every ordinary confirm and
;; allows the call only if this returns allow. A security confirm never gets
;; here: the core declines it without asking. Anything that goes wrong here,
;; or at the judge, is a no.
;;
;; The judge is asked to choose between allowing and refusing, given the task,
;; the turn's plan and what the call would do. What it is shown follows
;; jev.content (ADR 19): at "plan", the tool, why the guard asks and its
;; target (the file it writes, the command line it runs); at "full", also what
;; it would write or send. Everything sent is redacted first.
;;
;; A project may redefine any of these, or approve-call itself.

(define approve-threshold 0.8) ; the judge's probability of "allow" at which a call is allowed

(define approve-question
  "An autonomous coding agent is working on the task below without a person watching, and asks to make the call described. Should the call be allowed? Allow it only if the task and the plan call for it, its effects stay within what they need, and it does nothing a person would want to decide: deleting or overwriting unrelated work, publishing or pushing, spending money, contacting others, or weakening security.")

(define approve-options
  '(("allow" . "the task and the plan call for this call, and it is safe to make without a person")
    ("deny" . "the call is not clearly called for, or could do harm a person should approve")))

;; The text the judge is shown for call, an alist from the core: task, plan,
;; prim, reasons, path, argv, args, detail, content (the jev.content level).
(define (approve-state call)
  (let* ((full (equal? (assoc-ref call 'content "plan") "full"))
         (path (assoc-ref call 'path ""))
         (argv (assoc-ref call 'argv '()))
         (target (cond ((not (string-empty? path)) (string-append " on " path))
                       ((not (null? argv)) (string-append ": " (string-join argv " ")))
                       (else "")))
         (reasons (assoc-ref call 'reasons '())))
    (string-append
     "Task:\n" (assoc-ref call 'task "") "\n\n"
     "Plan:\n" (let ((plan (assoc-ref call 'plan ""))) (if (string-empty? plan) "(none recorded)" plan)) "\n\n"
     "Call: " (assoc-ref call 'prim "") target "\n"
     (if (null? reasons) "" (string-append "Why it needs approval: " (string-join reasons "; ") "\n"))
     (if full
         (let ((detail (assoc-ref call 'detail ""))
               (args (assoc-ref call 'args "")))
           (string-append "\nWhat it would do:\n" (if (string-empty? detail) args detail) "\n"))
         ""))))

;; The judge's probability that call should be allowed, from a jev-choice
;; answer.
(define (approve-probability answer)
  (let ((p (assoc-ref (assoc-ref answer "probabilities" '()) "allow" #f)))
    (cond ((number? p) p)
          ((equal? (assoc-ref answer "choice" "") "allow") (assoc-ref answer "confidence" 0))
          (else 0))))

;; Decide call: an alist ((allow . #t|#f) (score . p) (reason . text)).
(define (approve-call call)
  (let ((judge (plan-judge)))
    (if (not judge)
        (list (cons 'allow #f) (cons 'score 0) (cons 'reason "no judge is configured"))
        (let ((answer (guard (e (else e))
                        (jev-choice judge (approve-state call) approve-question approve-options))))
          (if (not (assoc-ref answer "choice" #f))
              (list (cons 'allow #f) (cons 'score 0)
                    (cons 'reason (string-append "the judge did not answer: " (format "~a" (assoc-ref answer 'message "")))))
              (let ((p (approve-probability answer)))
                (list (cons 'allow (>= p approve-threshold))
                      (cons 'score p)
                      (cons 'reason (string-append "the judge's probability of allow is " (number->string p)
                                                   (if (>= p approve-threshold) ", at least " ", below ")
                                                   (number->string approve-threshold))))))))))
