;; The optimizer, written as user code (docs/scheme.md §9.4): a sub-agent
;; that reads this repository's past sessions and improves how nibb works
;; here by editing .nibb/init.scm. The user starts it with a message:
;;
;;   /optimize                 every session of this repository
;;   /optimize 5               the latest five
;;   /optimize ID ID ...       the sessions named
;;
;; and any other words after it are a focus ("/optimize 5 test runs"). Its
;; writes are limited to .nibb/, and every one prompts (a protected path), so
;; it needs a person: an unattended run (--yolo) refuses those writes.
;; Once it is done the init file is reloaded; if the reload fails, the file is
;; restored. The model then tells the user what changed.

;; Characters of history the optimizer reads per session, and in all.
(define optimize-session-chars 6000)
(define optimize-total-chars 60000)

(define (clip s n)
  (if (> (string-length s) n) (string-append (substring s 0 n) " [...]") s))

(define (payload-ref ev key)
  (let ((p (assoc-ref ev 'payload #f)))
    (if (pair? p) (assoc-ref p key "") "")))

;; One event as a line of the digest, or #f to leave it out.
(define (digest-line ev)
  (let ((kind (assoc-ref ev 'kind)))
    (cond ((equal? kind "user.msg") (string-append "USER: " (clip (format "~a" (payload-ref ev "text")) 800)))
          ((equal? kind "harness.msg") (string-append "STRATEGY: " (clip (format "~a" (payload-ref ev "text")) 300)))
          ((equal? kind "llm.resp")
           (let ((calls (payload-ref ev "tool_calls"))
                 (text (format "~a" (payload-ref ev "content"))))
             (if (and (pair? calls) (not (null? calls)))
                 (string-append "CALLS: "
                                (string-join (map (lambda (c) (format "~a" (assoc-ref c "name" "?"))) calls) " "))
                 (and (not (string-empty? (string-trim text)))
                      (string-append "ANSWER: " (clip text 600))))))
          ((equal? kind "tool.result")
           (and (not (payload-ref ev "ok"))
                (string-append "FAILED " (format "~a" (payload-ref ev "name")) ": "
                               (clip (format "~a" (payload-ref ev "error")) 200))))
          ((equal? kind "jev.req") (string-append "JUDGE: " (clip (format "~a" (payload-ref ev "answer")) 200)))
          ((equal? kind "context.narrow")
           (string-append "SUB-AGENT " (format "~a" (payload-ref ev "agent")) " starts"))
          ((equal? kind "note") (string-append "NOTE: " (clip (format "~a" (payload-ref ev "text")) 200)))
          ((equal? kind "error") (string-append "ERROR: " (clip (format "~a" (payload-ref ev "error")) 200)))
          ((equal? kind "turn.end") (string-append "TURN END: " (format "~a" (payload-ref ev "outcome"))))
          (else #f))))

(define optimize-kinds
  '("user.msg" "harness.msg" "llm.resp" "tool.result" "jev.req" "context.narrow" "note" "error" "turn.end"))

;; A session as text: what the user asked, what the model did and answered,
;; what failed or was refused, what judges and sub-agents said.
(define (session-digest id)
  (let ((lines (filter (lambda (l) l) (map digest-line (session-events id optimize-kinds)))))
    (clip (string-append "== session " id "\n" (string-join lines "\n")) optimize-session-chars)))

;; The sessions a selector names: #f for all, a number for the latest n, or a
;; list of ids. The session running the optimizer is left out.
(define (optimize-sessions selector)
  (let ((listed (filter (lambda (s) (not (assoc-ref s 'current)))
                        (if (number? selector) (session-list (+ selector 1)) (session-list)))))
    (map (lambda (s) (assoc-ref s 'id))
         (cond ((pair? selector) (filter (lambda (s) (member (assoc-ref s 'id) selector)) listed))
               ((number? selector) (if (> (length listed) selector) (list-head listed selector) listed))
               (else listed)))))

(define (list-head l n)
  (if (or (= n 0) (null? l)) '() (cons (car l) (list-head (cdr l) (- n 1)))))

;; Digests of the selected sessions, newest first, within the total budget.
(define (optimize-history ids)
  (let loop ((ids ids) (out '()) (used 0))
    (if (null? ids)
        (string-join (reverse out) "\n\n")
        (let ((d (session-digest (car ids))))
          (if (> (+ used (string-length d)) optimize-total-chars)
              (string-join (reverse out) "\n\n")
              (loop (cdr ids) (cons d out) (+ used (string-length d))))))))

;; The optimizer may read anything in the repository and write only under
;; .nibb/.
(define (optimizer-may-call? call)
  (let ((name (assoc-ref call 'name)))
    (cond ((member name '("fs-read" "fs-list" "index-grep" "fs-find")) #t)
          ((member name '("fs-write" "fs-edit"))
           (string-prefix? ".nibb/" (format "~a" (assoc-ref (json-parse (assoc-ref call 'args "{}")) "path" ""))))
          (else #f))))

(define (read-init)
  (guard (e (else #f)) (fs-read ".nibb/init.scm")))

;; Run the optimizer over the selected sessions with an optional focus, then
;; reload. Returns the text the model reports from.
(define (optimize selector focus)
  (let ((ids (optimize-sessions selector)))
    (if (null? ids)
        "There are no earlier sessions of this repository to learn from."
        (let* ((before (read-init))
               (answer (sub-agent (optimizer-system-prompt)
                                  (optimizer-task (optimize-history ids) focus)
                                  optimizer-may-call? "optimizer"))
               (after (read-init)))
          (string-append
           "The optimizer read " (number->string (length ids)) " sessions and reported:\n" answer "\n\n"
           (cond ((equal? before after) "It did not change .nibb/init.scm.")
                 (else
                  (guard (e (else
                             (if before (fs-write ".nibb/init.scm" before) (fs-write ".nibb/init.scm" ""))
                             (string-append "Reloading .nibb/init.scm failed, so it was restored: "
                                            (format "~a" (assoc-ref e 'message "")))))
                    (let ((notes (reload)))
                      (string-append ".nibb/init.scm was changed and reloaded; the new strategy applies from the next turn."
                                     (if (null? notes) "" (string-append "\nFixed for this session, applying from the next one:\n- "
                                                                           (string-join notes "\n- ")))))))))))))

;; "/optimize [n | id ...] [focus words]" as (selector . focus).
(define (parse-optimize request)
  (let loop ((words (cdr (filter (lambda (w) (not (string-empty? w))) (string-split (string-trim request) " "))))
             (n #f) (ids '()) (focus '()))
    (cond ((null? words)
           (cons (cond (n n) ((pair? ids) (reverse ids)) (else #f))
                 (string-join (reverse focus) " ")))
          ((and (not n) (null? ids) (null? focus) (string->number (car words)))
           (loop (cdr words) (string->number (car words)) ids focus))
          ((and (null? focus) (string-contains? (car words) "T") (string-contains? (car words) "-")
                (> (string-length (car words)) 16))
           (loop (cdr words) n (cons (car words) ids) focus))
          (else (loop (cdr words) n ids (cons (car words) focus))))))

(define (optimize-request? request)
  (string-prefix? "/optimize" (string-trim request)))

;; What agent-turn does with an /optimize request: run the optimizer, then
;; let the model tell the user what it found and changed.
(define (optimize-turn request)
  (let* ((parsed (parse-optimize request))
         (report (optimize (car parsed) (cdr parsed))))
    (add-message (string-append report "\n\nTell the user briefly what the optimizer found and what changed."))
    (model-turn)))
