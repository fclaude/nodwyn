;; Context compaction, written as user code over the loop's primitives
;; (docs/scheme.md §9.2). When the context grows past compact-threshold
;; tokens, the model writes a handover note, and the turn goes on in a
;; narrowed context holding only the task and that note. The narrowing
;; lasts: later turns start from the note too, not from the whole
;; conversation again. The log keeps the whole conversation, so replay and
;; audit see everything.
;; On by default, late: just under the window's high water, so only work
;; that really runs long compacts (ADR 32). Compacting at half that, runs
;; that would have finished normally stopped to write notes and spent
;; minutes re-orienting from them. A project sets its own with, say,
;; (define compact-threshold 40000) in .nodwyn/init.scm, or #f for never.
;;
;; The context is measured as the window measures it before trimming: its
;; estimate of the next request, (context-tokens), in the units of
;; (window-limits). Not the server's prompt-tokens for the last request:
;; a tokenizer may count a fifth fewer than the estimate, and the last
;; request lacks the tool results just added, so the window trimmed old
;; reads away before that count ever reached the threshold. The note
;; request may run past the high water up to the window's ceiling, the room
;; the model's context has left above it, so a round that jumps past the
;; high water is still compacted rather than trimmed. A context no bigger
;; than the window (the default 60k, a local model) has no such room, and
;; such a jump trims before the note is written.
;;
;; The user compacts whenever they like, whatever the context's size, with a
;; message:
;;
;;   /compact                    compact now
;;   /compact the failing tests  any words after it are what the note must keep
;;
;; and a strategy asks for it at the next round with (compact-next-round).

;; Context tokens past which the context is compacted; #f never compacts.
;; The default leaves a twenty-fifth of the high water: 58983 at the
;; default 61440, and the note still fits in the reply reserve.
(define compact-threshold
  (let ((high (assoc-ref (window-limits) 'high-water 0)))
    (and (> high 0) (- high (quotient high 25)))))

;; Replies to the note request that may be tool calls before compaction
;; gives up for now.
(define compact-attempts 2)

;; The context size at which compaction last gave up: it is not tried again
;; until the context has grown by another quarter of the threshold.
(define compact-gave-up-at 0)

;; A compaction asked for with compact-next-round: #f, or what the note must
;; keep ("" for nothing in particular).
(define compact-requested #f)

;; Compact at the next round, whatever the context's size: maybe-compact
;; does it where the loop next calls it, in this turn or the next. An
;; optional focus says what the note must keep.
(define (compact-next-round . focus)
  (set! compact-requested (if (pair? focus) (car focus) "")))

;; Ask for the handover note, with the room above the high water: its text,
;; or #f if the model kept calling tools or wrote nothing. An optional
;; focus, when not empty, says what it must keep.
(define (compact-note . focus)
  (let ((keep (if (pair? focus) (string-trim (car focus)) "")))
    (add-message (if (string-empty? keep)
                     compact-summary-prompt
                     (string-append compact-summary-prompt "\n" (compact-focus-prompt keep)))))
  (let ask ((left compact-attempts))
    (let* ((reply (model-turn "control" '((headroom . #t))))
           (calls (assoc-ref reply 'tool-calls '()))
           (note (string-trim (assoc-ref reply 'content ""))))
      (cond ((pair? calls)
             (skip-tools calls "write the handover note first; tools are off for it")
             (and (> left 1) (ask (- left 1))))
            ((string-empty? note) #f)
            (else note)))))

;; Compact now: #t once the turn continues in the narrowed context, #f if no
;; note came back and the turn goes on in the full one. The narrowing lasts
;; past the turn, so the next turns start from the note as well. An optional
;; focus says what the note must keep.
(define (compact-context request . focus)
  (let ((note (apply compact-note focus)))
    (if note
        (begin (narrow-context (session-system-prompt) "compaction" '((lasting . #t)))
               (add-message (compact-handover-prompt request note))
               #t)
        (begin (add-message "Never mind the handover note; continue the task.")
               #f))))

;; Compact if a strategy asked for it (compact-next-round), or if the context
;; the next model-turn would send passed the threshold. Call it once the
;; latest reply's tool calls are answered; the reply itself is not read,
;; since the context is measured as it stands.
(define (maybe-compact request reply)
  (let ((focus compact-requested))
    (if focus
        (begin (set! compact-requested #f)
               (compact-context request focus))
        (let ((size (context-tokens)))
          (and compact-threshold
               (> size compact-threshold)
               (> size (+ compact-gave-up-at (quotient compact-threshold 4)))
               (or (compact-context request)
                   (begin (set! compact-gave-up-at size) #f)))))))

(define (compact-request? request)
  (let ((r (string-trim request)))
    (or (equal? r "/compact") (string-prefix? "/compact " r) (string-prefix? "/compact\n" r))))

;; The words after /compact: what the note must keep.
(define (compact-focus request)
  (let ((r (string-trim request)))
    (string-trim (substring r (string-length "/compact") (string-length r)))))

;; The request a compaction between turns hands over: the user's latest
;; message that is not itself a /compact, or #f if there is none.
(define (compact-last-request)
  (let loop ((rs (reverse (session-requests))))
    (cond ((null? rs) #f)
          ((compact-request? (car rs)) (loop (cdr rs)))
          (else (car rs)))))

;; What agent-turn does with a /compact request: compact now, whatever the
;; context's size, handing over the user's latest request before it, then
;; end the turn. The next message goes on from the handover note; the model
;; is not asked to go on before then, so it does not start on what the note
;; says is left without being asked.
(define (compact-turn request)
  (let ((task (compact-last-request)))
    (cond ((not task)
           (log-note "Nothing to compact: there is no request before this one."))
          ((compact-context task (compact-focus request))
           (log-note "Compacted: the conversation goes on from the handover note."))
          (else
           (log-note "Not compacted: the model wrote no handover note.")))))
