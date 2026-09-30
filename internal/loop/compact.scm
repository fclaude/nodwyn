;; Context compaction, written as user code over the loop's primitives
;; (docs/scheme.md §9.2). When a model-turn's prompt grows past
;; compact-threshold tokens, the model writes a handover note, and the turn
;; goes on in a narrowed context holding only the task and that note. The
;; log keeps the whole conversation, so replay and audit see everything.
;; On by default, late: just under the window's high water, so only work
;; that really runs long compacts (ADR 32). Compacting at half that, runs
;; that would have finished normally stopped to write notes and spent
;; minutes re-orienting from them. A project sets its own with, say,
;; (define compact-threshold 40000) in .nodwyn/init.scm, or #f for never.

;; Prompt tokens past which the context is compacted; #f never compacts.
;; The default leaves a twenty-fifth of the high water: 58983 at the
;; default 61440, and the note still fits in the reply reserve.
(define compact-threshold
  (let ((high (assoc-ref (window-limits) 'high-water 0)))
    (and (> high 0) (- high (quotient high 25)))))

;; Replies to the note request that may be tool calls before compaction
;; gives up for now.
(define compact-attempts 2)

;; The prompt size at which compaction last gave up: it is not tried again
;; until the context has grown by another quarter of the threshold.
(define compact-gave-up-at 0)

;; Ask for the handover note: its text, or #f if the model kept calling tools
;; or wrote nothing.
(define (compact-note)
  (add-message compact-summary-prompt)
  (let ask ((left compact-attempts))
    (let* ((reply (model-turn))
           (calls (assoc-ref reply 'tool-calls '()))
           (note (string-trim (assoc-ref reply 'content ""))))
      (cond ((pair? calls)
             (skip-tools calls "write the handover note first; tools are off for it")
             (and (> left 1) (ask (- left 1))))
            ((string-empty? note) #f)
            (else note)))))

;; Compact now: #t once the turn continues in the narrowed context, #f if no
;; note came back and the turn goes on in the full one.
(define (compact-context request)
  (let ((note (compact-note)))
    (if note
        (begin (narrow-context (session-system-prompt))
               (add-message (compact-handover-prompt request note))
               #t)
        (begin (add-message "Never mind the handover note; continue the task.")
               #f))))

;; Compact if reply's prompt passed the threshold. Call it with the latest
;; model-turn's reply once its tool calls are answered.
(define (maybe-compact request reply)
  (let ((size (assoc-ref reply 'prompt-tokens 0)))
    (and compact-threshold
         (> size compact-threshold)
         (> size (+ compact-gave-up-at (quotient compact-threshold 4)))
         (or (compact-context request)
             (begin (set! compact-gave-up-at size) #f)))))
