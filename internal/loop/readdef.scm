;; read-definition: read a function or class by name, and a hint when a shell
;; script reads a file (ADR 32; after hygiene.scm, which folds its results
;; like reads). On the benchmark's dev set, 15 of the 16 Python scripts that
;; opened a source file in a run with hygiene found a
;; definition by regular expression and printed its body: one call where the
;; file tools take two (index-grep for the line, then fs-read with a guessed
;; count). read-definition is that call. A script that reads a file to print
;; it gets a one-line hint naming the file tools, a few times per session;
;; scripts that load, inspect or parse the code are left alone.

(define readdef-max-lines 200)   ; lines of a definition shown; a longer one shows its members
(define readdef-read-lines 600)  ; lines read from the definition's start to find its end
(define readdef-hints 3)         ; script-read hints per session
(define readdef-hints-given 0)

(define readdef-name-chars "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_")

(define (readdef-identifier? s)
  (and (not (string-empty? s))
       (let walk ((i 0))
         (or (= i (string-length s))
             (and (string-contains? readdef-name-chars (substring s i (+ i 1)))
                  (walk (+ i 1)))))))

;; A line that defines name, across common languages: def, class, func (with
;; a Go receiver), function, fn, struct, enum, trait, interface, impl, type,
;; module, and const/let/var bindings, after modifiers such as export or pub.
(define (readdef-pattern name)
  (string-append
   """^\s*(?:(?:export|default|pub(?:\([a-z]+\))?|public|private|protected|static|final|abstract|async|unsafe|extern|override|open|data|sealed|inline)\s+)*"""
   """(?:def|class|func|function\*?|fn|struct|enum|trait|interface|impl|type|module|object|const|let|var|val)\s+(?:\([^)]*\)\s*)?"""
   name
   """\b"""))

(define (readdef-list-head l n) (if (or (<= n 0) (null? l)) '() (cons (car l) (readdef-list-head (cdr l) (- n 1)))))

(define (readdef-indent line)
  (let walk ((i 0) (n 0))
    (cond ((>= i (string-length line)) n)
          ((equal? (substring line i (+ i 1)) " ") (walk (+ i 1) (+ n 1)))
          ((equal? (substring line i (+ i 1)) "\t") (walk (+ i 1) (+ n 4)))
          (else n))))

(define (readdef-count s sub) (- (length (string-split s sub)) 1))

(define (readdef-blank? line) (string-empty? (string-trim line)))

(define (readdef-braces? path)
  (not (or (string-suffix? ".py" path) (string-suffix? ".pyi" path) (string-suffix? ".pyx" path))))

;; How many of lines (the definition's line first) belong to it: until the
;; braces opened on its first lines close, or, by indentation, until a line
;; indented no deeper than the first, outside any open parenthesis (a
;; signature over several lines). #f when the block runs past the lines.
(define (readdef-extent lines braces)
  (let ((base (readdef-indent (car lines))))
    (if (and braces (let any ((ls lines) (k 0))
                      (and (pair? ls) (< k 5)
                           (or (string-contains? (car ls) "{") (any (cdr ls) (+ k 1))))))
        (let walk ((ls lines) (n 0) (depth 0) (opened #f))
          (if (null? ls)
              #f
              (let* ((d (+ depth (readdef-count (car ls) "{") (- (readdef-count (car ls) "}"))))
                     (o (or opened (> d 0))))
                (if (and o (<= d 0)) (+ n 1) (walk (cdr ls) (+ n 1) d o)))))
        (let walk ((ls (cdr lines)) (n 1)
                   (parens (- (readdef-count (car lines) "(") (readdef-count (car lines) ")"))))
          (cond ((null? ls) #f)
                ((and (<= parens 0) (not (readdef-blank? (car ls))) (<= (readdef-indent (car ls)) base))
                 ;; the block ends before this line, without its trailing blank lines
                 (let back ((k n) (rest (reverse (readdef-list-head lines n))))
                   (if (and (> k 1) (readdef-blank? (car rest))) (back (- k 1) (cdr rest)) k)))
                (else (walk (cdr ls) (+ n 1)
                            (+ parens (readdef-count (car ls) "(") (- (readdef-count (car ls) ")"))))))))))

;; Whether a ranged fs-read's text "[lines A-B of N]" reached the end (B = N).
(define (readdef-eof? text)
  (let* ((head (car (string-split text "]")))
         (parts (string-split (substring head 7 (string-length head)) " of ")))
    (and (pair? (cdr parts))
         (equal? (cadr (string-split (car parts) "-")) (cadr parts)))))

;; Lines to the end of a file make a whole block, without trailing blanks.
(define (readdef-to-end lines)
  (let back ((k (length lines)) (rest (reverse lines)))
    (if (and (> k 1) (readdef-blank? (car rest))) (back (- k 1) (cdr rest)) k)))

;; The first line number and the lines of a ranged fs-read's text.
(define (readdef-range text)
  (let* ((lines (string-split text "\n"))
         (header (car lines))
         (first (or (string->number (car (string-split (substring header 7 (string-length header)) "-"))) 1))
         (body (cdr lines)))
    (cons first (if (and (pair? body) (string-empty? (car (reverse body)))) (reverse (cdr (reverse body))) body))))

;; Decorators and attributes right above the definition, at its indentation.
(define (readdef-decorators above indent)
  (let walk ((ls (reverse above)) (n 0))
    (if (and (pair? ls) (let ((t (string-trim (car ls))))
                          (and (or (string-prefix? "@" t) (string-prefix? "#[" t))
                               (= (readdef-indent (car ls)) indent))))
        (walk (cdr ls) (+ n 1))
        n)))

(define readdef-member-words
  '("def " "async def " "class " "func " "function " "fn " "pub fn " "public " "private " "protected " "static "))

;; A long definition's members: the definition lines inside it, with numbers.
(define (readdef-members lines first)
  (let ((inner (+ (readdef-indent (car lines)) 1)))
    (let walk ((ls (cdr lines)) (n (+ first 1)) (out '()))
      (if (null? ls)
          (reverse out)
          (let ((t (string-trim (car ls))))
            (walk (cdr ls) (+ n 1)
                  (if (and (>= (readdef-indent (car ls)) inner)
                           (let any ((ws readdef-member-words))
                             (and (pair? ws) (or (string-prefix? (car ws) t) (any (cdr ws))))))
                      (cons (string-append "  " (number->string n) ": " t) out)
                      out)))))))

;; The definition at path:line as a ranged read: its decorators and lines, or
;; for a long one its first lines and its members.
(define (readdef-show path line)
  (let* ((start (max 1 (- line 8)))
         (text (fs-read path start (+ readdef-read-lines 8)))
         (range (readdef-range text))
         (first (car range))
         (all (cdr range))
         (above (readdef-list-head all (- line first)))
         (lines (list-tail all (min (length all) (- line first)))))
    (if (null? lines)
        (string-append "Could not read " path " at line " (number->string line) ".")
        (let* ((deco (readdef-decorators above (readdef-indent (car lines))))
               (from (- line deco))
               (extent (or (readdef-extent lines (readdef-braces? path))
                           (and (readdef-eof? text) (readdef-to-end lines))))
               (shown (append (list-tail above (- (length above) deco))
                              (readdef-list-head lines (min (or extent (length lines)) readdef-max-lines))))
               (to (+ from (length shown) -1)))
          (string-append
           "[lines " (number->string from) "-" (number->string to) " of " path "]\n"
           (string-join shown "\n")
           (if (and extent (<= extent readdef-max-lines))
               ""
               (let ((members (readdef-members lines line)))
                 (string-append
                  "\n[the definition continues"
                  (if extent (string-append " to line " (number->string (+ line extent -1))) " past this read")
                  "; read more with fs-read (path, start, count)"
                  (if (null? members) "]" (string-append ". Its members:]\n" (string-join members "\n")))))))))))

(define (readdef-matches path pattern)
  (map (lambda (m) (cons (format "~a" (assoc-ref m "path" path)) (assoc-ref m "line" 1)))
       (assoc-ref (index-grep path pattern) "matches" '())))

(define (readdef-test-path? p) (or (string-contains? p "test") (string-contains? p "spec")))

;; Where an owner (a class, struct or module) starts and ends: (path first
;; last); one longer than the read reaches to the end of the file.
(define (readdef-block path line)
  (let* ((text (fs-read path line readdef-read-lines))
         (range (readdef-range text))
         (extent (or (readdef-extent (cdr range) (readdef-braces? path))
                     (and (readdef-eof? text) (readdef-to-end (cdr range))))))
    (list path line (if extent (+ line extent -1) 1000000000))))

(define (readdef-inside? m blocks)
  (let any ((bs blocks))
    (and (pair? bs)
         (or (and (equal? (car m) (car (car bs))) (> (cdr m) (cadr (car bs))) (<= (cdr m) (caddr (car bs))))
             (any (cdr bs))))))

(define (readdef-find path name)
  (let* ((parts (string-split name "."))
         (leaf (car (reverse parts))))
    (if (not (let all ((ps parts)) (or (null? ps) (and (readdef-identifier? (car ps)) (all (cdr ps))))))
        "Give a name such as parse_args, Figure or Figure.subfigures (letters, digits and _, owner first)."
        (let* ((found (readdef-matches path (readdef-pattern leaf)))
               (found (if (= (length parts) 1)
                          found
                          (let ((owners (map (lambda (o) (readdef-block (car o) (cdr o)))
                                             (readdef-matches path (readdef-pattern (list-ref parts (- (length parts) 2)))))))
                            (filter (lambda (m) (readdef-inside? m owners)) found))))
               (found (append (filter (lambda (m) (not (readdef-test-path? (car m)))) found)
                              (filter (lambda (m) (readdef-test-path? (car m))) found))))
          (if (null? found)
              (string-append "No definition of " name " found in " path
                             ": it may be assigned rather than defined, or defined elsewhere: try index-grep.")
              (string-append
               (readdef-show (car (car found)) (cdr (car found)))
               (if (null? (cdr found))
                   ""
                   (string-append "\n[also defined at: "
                                  (string-join (map (lambda (m) (string-append (car m) ":" (number->string (cdr m))))
                                                    (readdef-list-head (cdr found) 10))
                                               ", ")
                                  (if (> (length (cdr found)) 10) ", ..." "")
                                  "]"))))))))

(define-agent-tool "read-definition"
  "Read a function, method or class by name: its whole definition and line range, found with index-grep and read with fs-read, in one call. For a long class it shows the start and the members. Use it instead of a script that searches a file for a definition and prints it."
  '(("path" "string" "a repository file, or a directory to search")
    ("name" "string" "the name, such as parse_args, or owner and name, such as Figure.subfigures"))
  (lambda (call args)
    (guard (e (else (string-append "read-definition failed: " (format "~a" (assoc-ref e 'message "")))))
      (readdef-find (format "~a" (assoc-ref args "path" ".")) (string-trim (format "~a" (assoc-ref args "name" "")))))))

;; A shell command that reads a repository file only to print it: a Python
;; script that opens a file and prints, importing only text and path modules
;; (none that load the repository's code, inspect or parse it); or sed, cat,
;; head or tail on a file.
(define readdef-plain-modules '("re" "os" "sys" "io" "json" "textwrap" "pathlib" "itertools" "collections"))

(define (readdef-plain-imports? text)
  (let each ((ss (apply append (map (lambda (l) (string-split l ";")) (string-split text "\n")))))
    (or (null? ss)
        (let* ((st (string-trim (car ss)))
               (mods (cond ((string-prefix? "import " st)
                            (map (lambda (m) (car (string-split (car (string-split (string-trim m) " ")) ".")))
                                 (string-split (substring st 7 (string-length st)) ",")))
                           ((string-prefix? "from " st)
                            (list (car (string-split (car (string-split (substring st 5 (string-length st)) " ")) "."))))
                           (else '()))))
          (and (let all ((ms mods)) (or (null? ms) (and (member (car ms) readdef-plain-modules) (all (cdr ms)))))
               (each (cdr ss)))))))

(define (readdef-script-read? call)
  (and (equal? (assoc-ref call 'name) "shell-exec")
       (let* ((a (guard (e (else '())) (json-parse (format "~a" (assoc-ref call 'args "{}")))))
              (cmd (format "~a" (if (pair? a) (assoc-ref a "cmd" "") "")))
              (argv (if (pair? a) (assoc-ref a "args" '()) '()))
              (argv (if (list? argv) (map (lambda (x) (format "~a" x)) argv) '()))
              (code (let find ((as argv)) (cond ((null? as) #f) ((equal? (car as) "-c") (and (pair? (cdr as)) (cadr as))) (else (find (cdr as))))))
              (text (or code (string-join argv " ")))
              (base (car (reverse (string-split cmd "/")))))
         (or (and (string-contains? (string-append cmd " " text) "python")
                  (or (string-contains? text "open(") (string-contains? text "read_text("))
                  (string-contains? text "print")
                  (not (string-contains? text "exec("))
                  (readdef-plain-imports? text))
             (and (member base '("sed" "cat" "head" "tail"))
                  (not (string-contains? text "|")))))))

(define readdef-hint
  "\n[hint: read-definition (path, name) returns a function or class by name, and fs-read (path, start, count) reads lines of a file; neither needs a script]")

;; The path in a read-definition result's header "[lines A-B of PATH]".
(define (readdef-header-path text)
  (let ((head (car (string-split text "]"))))
    (let ((parts (string-split head " of ")))
      (if (pair? (cdr parts)) (cadr parts) ""))))

(define readdef-base-run-calls run-calls)
(define (run-calls calls)
  (let ((results (readdef-base-run-calls calls)))
    (when (= (guard (e (else 0)) hygiene-depth) 0)
      (for-each
       (lambda (c r)
         (cond ((and (equal? (assoc-ref c 'name) "read-definition") (assoc-ref r 'ok #f))
                ;; hygiene folds a definition as it folds any read
                (let ((text (format "~a" (assoc-ref r 'value ""))))
                  (when (and (string-prefix? "[lines " text)
                             (guard (e (else #f)) (list? hygiene-reads))
                             (>= (string-length text) hygiene-min-read))
                    (set! hygiene-reads
                          (cons (list (assoc-ref c 'id) (readdef-header-path text) (hygiene-first-line text) text hygiene-round)
                                hygiene-reads)))))
               ((and (< readdef-hints-given readdef-hints) (assoc-ref r 'ok #f) (readdef-script-read? c))
                (set! readdef-hints-given (+ readdef-hints-given 1))
                (fold-result (assoc-ref c 'id) (string-append (format "~a" (assoc-ref r 'value "")) readdef-hint)))))
       calls results))
    results))

