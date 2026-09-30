;; Go toolchain tools for building nodwyn with nodwyn. Each one is a named,
;; documented wrapper over shell-exec; the guard sees both the wrapper and
;; the shell-exec inside it, and .nodwyn/allow.toml holds the standing
;; approvals that let these run without a prompt.

;; run: execute and render for the model — exit status, then stdout and
;; stderr as they were produced.
(define (run cmd args)
  (let ((r (shell-exec cmd args)))
    (format "exit ~a~%~a~a"
            (assoc-ref r "status")
            (assoc-ref r "stdout")
            (assoc-ref r "stderr"))))

(register-tool "go-test"
  (lambda (pkg) (run "go" (list "test" "-race" pkg)))
  '((doc . "Run the Go tests with the race detector for a package pattern such as ./internal/log or ./... . Returns the exit status and output.")
    (params . (("pkg" "string" "package pattern, e.g. ./internal/log or ./...")))))

(register-tool "go-build"
  (lambda () (run "go" '("build" "./...")))
  '((doc . "Compile every package. Returns the exit status and any compiler errors.")
    (params . ())))

(register-tool "go-vet"
  (lambda () (run "go" '("vet" "./...")))
  '((doc . "Run go vet on every package.")
    (params . ())))

(register-tool "go-fmt-check"
  (lambda () (run "gofmt" '("-l" ".")))
  '((doc . "List Go files that are not gofmt-formatted (empty output means all formatted). Does not modify files.")
    (params . ())))

(register-tool "run-make"
  (lambda (target) (run "make" (list target)))
  '((doc . "Run a Makefile target: build, test, lint, exfil or accept.")
    (params . (("target" "string" "the make target")))))
