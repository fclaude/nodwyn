;; Finding the project's own environment (ADR 32). On a benchmark task
;; (requests-2931) five of six runs on the newer builds ran the tests with the
;; system Python, which lacks the project's dependencies, and worked around
;; it (patching the standard library, installing packages, copying modules)
;; instead of finding the environment the project is set up in; the run that
;; found it listed the directories where environments live. A line in the
;; system prompt says so, and a command that fails for a missing module gets
;; a hint, a few times per session.

(define envhint-hints 2)   ; missing-module hints per session
;; The lean profile gives none (2026-10-17): the hint was added in 99% of
;; sessions and plausibly mattered in 2 or 3 of 20, about half a call a task;
;; Pi has none (ADR 32, amended 2026-10-17).
(define lean-no-envhint #t)
(define envhint-given 0)

(define envhint-hint
  "\n[hint: if this interpreter lacks the project's dependencies, the project most likely has its own environment: look for it (a .venv or venv directory, `conda env list`, tox or nox settings, the test command in a Makefile or CI config) and use its interpreter, rather than installing or patching packages]")

;; A command's result that says a module is missing.
(define (envhint-missing-module? result)
  (let ((v (format "~a" (assoc-ref result 'value ""))))
    (or (string-contains? v "No module named")
        (string-contains? v "ModuleNotFoundError")
        (string-contains? v "cannot import name 'Mapping' from 'collections'")
        (string-contains? v "has no attribute 'MutableMapping'"))))

(define envhint-base-run-calls run-calls)
(define (run-calls calls)
  (let ((results (envhint-base-run-calls calls)))
    (for-each
     (lambda (c r)
       (when (and (not lean-no-envhint)
                  (< envhint-given envhint-hints)
                  (equal? (assoc-ref c 'name) "shell-exec")
                  (envhint-missing-module? r))
         (set! envhint-given (+ envhint-given 1))
         (fold-result (assoc-ref c 'id) (string-append (format "~a" (assoc-ref r 'value "")) envhint-hint))))
     calls results)
    results))

