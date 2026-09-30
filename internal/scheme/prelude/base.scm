;; nodwyn prelude — loaded into the global environment before anything else.
;; Documented in docs/scheme.md §6. Keep it small: anything here is part of
;; the language contract.

(define (assoc-ref alist key . default)
  (let ((hit (assoc key alist)))
    (cond (hit (cdr hit))
          ((null? default) (void))
          (else (car default)))))

(define (identity x) x)

(define (compose f g)
  (lambda args (f (apply g args))))

(define (string-empty? s)
  (= 0 (string-length s)))
