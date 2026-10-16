package scheme

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func eval(t *testing.T, in *Interp, src string) Value {
	t.Helper()
	v, err := in.EvalString(context.Background(), src)
	if err != nil {
		t.Fatalf("%s\n  error: %v", src, err)
	}
	return v
}

// TestConformance has one snippet per documented form and builtin
// (docs/scheme.md). want is the written form of the result.
func TestConformance(t *testing.T) {
	cases := []struct{ src, want string }{
		// §1 lexical syntax
		{`42`, `42`}, {`-7`, `-7`}, {`+3`, `3`}, {`3.14`, `3.14`}, {`1e3`, `1000.0`}, {`.5`, `0.5`},
		{`"a\"b\\c\n"`, `"a\"b\\c\n"`}, {`#t`, `#t`}, {`#f`, `#f`},
		{`'(a . b)`, `(a . b)`}, {`'(1 2 . 3)`, `(1 2 . 3)`}, {`'...`, `...`}, {`'1+`, `1+`},
		{"; comment\n 1 ; trailing\n", `1`},
		{"`(1 ,(+ 1 1) ,@(list 3 4))", `(1 2 3 4)`},
		{"`((doc . ,(string-append \"a\" \"b\")) (n . ,(+ 1 2)))", `((doc . "ab") (n . 3))`},
		{"`(1 . ,(list 2 3))", `(1 2 3)`},
		{"`(,@'() . ,(+ 1 1))", `2`},
		// §2 data types
		{`'()`, `()`}, {`(null? '())`, `#t`}, {`(boolean? #f)`, `#t`}, {`(integer? 1)`, `#t`}, {`(float? 1.0)`, `#t`},
		{`(number? 1.5)`, `#t`}, {`(string? "s")`, `#t`}, {`(symbol? 'a)`, `#t`}, {`(pair? '(1))`, `#t`},
		{`(list? '(1 2))`, `#t`}, {`(list? '(1 . 2))`, `#f`}, {`(procedure? car)`, `#t`}, {`(procedure? (lambda () 1))`, `#t`},
		{`(handle? 1)`, `#f`}, {`(void)`, `#<void>`}, {`car`, `#<primitive car>`},
		{`(define (f) 1) f`, `#<procedure f>`}, {`(define g (lambda () 1)) g`, `#<procedure g>`},
		{`(lambda () 1)`, `#<procedure>`}, {`1.0`, `1.0`}, {`(* 1.0 100)`, `100.0`},
		// §3 special forms
		{`(define x 1) x`, `1`}, {`(define (add a b) (+ a b)) (add 1 2)`, `3`},
		{`(define (rest a . r) r) (rest 1 2 3)`, `(2 3)`}, {`((lambda args args) 1 2)`, `(1 2)`},
		{`((lambda (a . r) (list a r)) 1)`, `(1 ())`},
		{`(if #t 1 2)`, `1`}, {`(if #f 1 2)`, `2`}, {`(if #f 1)`, `#<void>`}, {`(if 0 'yes 'no)`, `yes`},
		// §3 new special forms (P1b)
		{`(letrec ((a (lambda () (b 1))) (b (lambda (n) (* n 2)))) (a))`, `2`},
		{`(letrec ((f (lambda (n) (if (= n 0) 1 (* n (f (- n 1))))))) (f 4))`, `24`},
		{`(define (is-even? n) (if (= n 0) #t (is-odd? (- n 1)))) (define (is-odd? n) (if (= n 0) #f (is-even? (- n 1)))) (is-even? 10)`, `#t`},
		{`(case 2 ((1 2 3) 'in) ((4 5) 'other))`, `in`},
		{`(case 'foo ((bar baz) 'no) ((FOO foo) 'yes) (else 'fallback))`, `yes`},
		{`(case 'Foo ((foo) 'lower) (else 'exact))`, `exact`},
		{`(case "hi" (("hi" "there") 'match))`, `match`},
		{`(case 7 ((1) 'a) (else 'none))`, `none`},
		{`(case 7 ((1) 'a))`, `#<void>`},
		{`(do ((i 0 (+ i 1)) (out '() (cons i out))) ((= i 4) (reverse out)))`, `(0 1 2 3)`},
		{`(do ((i 1 (+ i 1)) (acc 1 (* acc i))) ((> i 5) acc))`, `120`},
		{`(do ((i 3 (- i 1))) ((< i 0) i))`, `-1`},
		{`(do ((i 0 (+ i 1))) ((= i 3)))`, `#<void>`},
		{`(with-output-to-string (do ((i 0 (+ i 1))) ((= i 3)) (display i)))`, `"012"`},
		{`(let ((v (vector 0 0 0))) (do ((i 0 (+ i 1))) ((= i 3) v) (vector-set! v i (* i i))))`, `#(0 1 4)`},
		{`(when #t "yes")`, `"yes"`},
		{`(when #f "nope")`, `#<void>`},
		{`(unless #f "runs")`, `"runs"`},
		{`(unless #t "skipped")`, `#<void>`},
		{`(guard (e (#t "recovered")) (error "boom"))`, `"recovered"`},
		{`(guard (e ((string-contains? (assoc-ref e 'message) "boom") 'caught)) (error "boom"))`, `caught`},
		{`(guard (e (#t 42)) (+ 1 2))`, `3`},
		{`(guard (e (#f 1) (#t 2)) (error "x"))`, `2`},
		{`(guard (e (else (assoc-ref e 'irritants))) (error "x" 1 "two"))`, `(1 "two")`},
		{`(guard (e ((assoc-ref e 'message) => string-length)) (error "four"))`, `4`},
		{`(guard (e (#t "caught last")) (display "hi") (error "last"))`, `"caught last"`},
		{`(guard (e (#t "no error")) (display "ok") (display "fine"))`, `#<void>`},
		{`(guard (e (#t (car e))) (car 1))`, `(message . "car: argument 1: expected pair, got integer 1")`},
		{`(guard (outer (#t 'outer)) (guard (inner (#f 'never)) (error "inner")))`, `outer`},
		{`(with-output-to-string (display "hi") (newline))`, `"hi\n"`},
		{`(with-output-to-string (write 42) (newline) (write 3.14))`, `"42\n3.14"`},
		{`(with-output-to-string (display '(1 2)))`, `"(1 2)"`},
		{`(with-output-to-string (display "A") (with-output-to-string (display "inner")) (display "B"))`, `"AB"`},
		{`(let ((a 1) (b 2)) (+ a b))`, `3`}, {`(let* ((a 1) (b (+ a 1))) b)`, `2`},
		{`(let loop ((i 0) (acc '())) (if (= i 3) (reverse acc) (loop (+ i 1) (cons i acc))))`, `(0 1 2)`},
		{`(begin 1 2 3)`, `3`}, {`(begin)`, `#<void>`},
		{`(define y 1) (set! y 2) y`, `2`},
		{`(quote (a b))`, `(a b)`}, {`'sym`, `sym`},
		{`(cond (#f 1) (else 2))`, `2`}, {`(cond ((assq 'b '((a 1) (b 2))) => cadr) (else 0))`, `2`},
		{`(cond (#f 1))`, `#<void>`}, {`(cond (5))`, `5`},
		{`(and)`, `#t`}, {`(and 1 2)`, `2`}, {`(and 1 #f 3)`, `#f`}, {`(or)`, `#f`}, {`(or #f 2)`, `2`}, {`(or #f #f)`, `#f`},
		{`(define (f) (define inner 5) (+ inner 1)) (f)`, `6`},
		{`(define let 1)`, `#<void>`}, // reserved names stay special forms
		// §5 numbers
		{`(+)`, `0`}, {`(+ 1 2 3)`, `6`}, {`(*)`, `1`}, {`(* 2 3.0)`, `6.0`}, {`(- 5)`, `-5`}, {`(- 10 1 2)`, `7`},
		{`(/ 6 3)`, `2`}, {`(/ 7 2)`, `3.5`}, {`(/ 2)`, `0.5`}, {`(/ 1.0 4)`, `0.25`},
		{`(= 1 1.0)`, `#t`}, {`(< 1 2 3)`, `#t`}, {`(< 1 3 2)`, `#f`}, {`(> 3 2 1)`, `#t`}, {`(<= 1 1 2)`, `#t`}, {`(>= 2 2 1)`, `#t`},
		{`(quotient 7 2)`, `3`}, {`(quotient -7 2)`, `-3`}, {`(remainder -7 2)`, `-1`}, {`(modulo -7 2)`, `1`}, {`(modulo 7 -2)`, `-1`},
		{`(abs -3)`, `3`}, {`(abs -2.5)`, `2.5`}, {`(min 3 1 2)`, `1`}, {`(max 1 2.0)`, `2.0`},
		{`(floor 2.5)`, `2.0`}, {`(ceiling 2.1)`, `3.0`}, {`(round 2.5)`, `2.0`}, {`(round 3.5)`, `4.0`}, {`(truncate -2.7)`, `-2.0`}, {`(floor 3)`, `3`},
		{`(exact 2.9)`, `2`}, {`(exact -2.9)`, `-2`}, {`(inexact 2)`, `2.0`},
		{`(expt 2 10)`, `1024`}, {`(expt 2 -1)`, `0.5`}, {`(expt 2.0 2)`, `4.0`}, {`(sqrt 16)`, `4.0`},
		{`(zero? 0)`, `#t`}, {`(zero? 0.0)`, `#t`}, {`(positive? 1)`, `#t`}, {`(negative? -1.5)`, `#t`}, {`(even? 4)`, `#t`}, {`(odd? 4)`, `#f`},
		{`(number->string 42)`, `"42"`}, {`(number->string 1.5)`, `"1.5"`}, {`(string->number "42")`, `42`}, {`(string->number "1e2")`, `100.0`}, {`(string->number "x")`, `#f`},
		// booleans and equivalence
		{`(not #f)`, `#t`}, {`(not 0)`, `#f`},
		{`(eq? 'a 'a)`, `#t`}, {`(eq? "a" "a")`, `#t`}, {`(eq? '(1) '(1))`, `#f`}, {`(eqv? 1 1)`, `#t`}, {`(eq? 1 1.0)`, `#f`},
		{`(let ((p '(1))) (eq? p p))`, `#t`}, {`(equal? '(1 (2 "x")) '(1 (2 "x")))`, `#t`}, {`(equal? 1 1.0)`, `#f`},
		// lists
		{`(cons 1 2)`, `(1 . 2)`}, {`(car '(1 2))`, `1`}, {`(cdr '(1 2))`, `(2)`},
		{`(caar '((1) 2))`, `1`}, {`(cadr '(1 2 3))`, `2`}, {`(cdar '((1 2)))`, `(2)`}, {`(cddr '(1 2 3))`, `(3)`}, {`(caddr '(1 2 3))`, `3`}, {`(cdddr '(1 2 3 4))`, `(4)`},
		{`(list 1 2)`, `(1 2)`}, {`(list)`, `()`}, {`(length '(1 2 3))`, `3`}, {`(append '(1) '(2 3) '())`, `(1 2 3)`}, {`(append)`, `()`}, {`(append '(1) 2)`, `(1 . 2)`},
		{`(reverse '(1 2 3))`, `(3 2 1)`}, {`(list-ref '(a b c) 1)`, `b`}, {`(list-tail '(a b c) 2)`, `(c)`}, {`(iota 4)`, `(0 1 2 3)`}, {`(iota 0)`, `()`},
		{`(memq 'b '(a b c))`, `(b c)`}, {`(memq 'z '(a))`, `#f`}, {`(member "b" '("a" "b"))`, `("b")`}, {`(member '(1) '((1) 2))`, `((1) 2)`},
		{`(assq 'b '((a . 1) (b . 2)))`, `(b . 2)`}, {`(assq 'z '())`, `#f`}, {`(assoc "k" '(("k" . 1)))`, `("k" . 1)`},
		{`(map (lambda (x) (* x x)) '(1 2 3))`, `(1 4 9)`}, {`(map + '(1 2) '(10 20 30))`, `(11 22)`},
		{`(let ((acc '())) (for-each (lambda (x) (set! acc (cons x acc))) '(1 2)) acc)`, `(2 1)`}, {`(for-each car '())`, `#<void>`},
		{`(filter odd? '(1 2 3 4))`, `(1 3)`}, {`(fold + 0 '(1 2 3))`, `6`}, {`(fold cons '() '(1 2))`, `((() . 1) . 2)`},
		{`(sort '(3 1 2) <)`, `(1 2 3)`}, {`(sort '("b" "a") string<?)`, `("a" "b")`},
		{`(apply + '(1 2))`, `3`}, {`(apply + 1 2 '(3))`, `6`}, {`(apply list '())`, `()`},
		// strings
		{`(string-length "héllo")`, `5`}, {`(string-append "a" "b" "c")`, `"abc"`}, {`(string-append)`, `""`},
		{`(substring "héllo" 1 3)`, `"él"`}, {`(substring "abc" 1)`, `"bc"`},
		{`(string=? "a" "a" "a")`, `#t`}, {`(string=? "a" "b")`, `#f`}, {`(string<? "a" "b" "c")`, `#t`},
		{`(string-upcase "aé")`, `"AÉ"`}, {`(string-downcase "AB")`, `"ab"`}, {`(string-trim "  x \n")`, `"x"`},
		{`(string-split "a,b,,c" ",")`, `("a" "b" "" "c")`}, {`(string-join '("a" "b") "-")`, `"a-b"`}, {`(string-join '() ",")`, `""`},
		{`(string-contains? "hello" "ell")`, `#t`}, {`(string-prefix? "he" "hello")`, `#t`}, {`(string-suffix? "lo" "hello")`, `#t`}, {`(string-suffix? "x" "hello")`, `#f`},
		{`(string-replace "a-b-c" "-" "+")`, `"a+b+c"`}, {`(string->symbol "ab")`, `ab`}, {`(symbol->string 'ab)`, `"ab"`},
		{`(format "~a and ~s~%~~" "x" "y")`, `"x and \"y\"\n~"`}, {`(format "plain")`, `"plain"`},
		// control
		{`(procedure? 1)`, `#f`},
		// json
		{`(json-parse "{\"a\":1,\"b\":[true,null,2.5],\"c\":\"s\"}")`, `(("a" . 1) ("b" #t null 2.5) ("c" . "s"))`},
		{`(json-parse "[]")`, `()`}, {`(json-parse "12345678901234")`, `12345678901234`},
		{`(json-emit '(("a" . 1) ("b" . (#t null 2.5)) ("c" . "s")))`, `"{\"a\":1,\"b\":[true,null,2.5],\"c\":\"s\"}"`},
		{`(json-emit '())`, `"[]"`}, {`(json-emit "x\"y")`, `"\"x\\\"y\""`}, {`(json-emit '(1 (2 3)))`, `"[1,[2,3]]"`},
		{`(json-emit (json-parse "{\"k\":{\"n\":[1,{\"m\":false}]}}"))`, `"{\"k\":{\"n\":[1,{\"m\":false}]}}"`},
		// meta
		{`(vector 1 "a" #t)`, `#(1 "a" #t)`}, {`(vector)`, `#()`},
		{`(vector? #(1 2))`, `#t`}, {`(vector? '(1 2))`, `#f`},
		{`(vector-length #(1 2 3))`, `3`}, {`(vector-length #())`, `0`},
		{`(vector-ref #(10 20 30) 1)`, `20`}, {`(vector-ref #(7) 0)`, `7`},
		{`(let ((v (vector 1 2 3))) (vector-set! v 1 99) (vector-ref v 1))`, `99`},
		{`(vector->list #(a b c))`, `(a b c)`}, {`(list->vector '(a b))`, `#(a b)`},
		{`(equal? #(1 2) #(1 2))`, `#t`}, {`(equal? #(1 2) #(1 3))`, `#f`},
		{`(let ((v #(1 2))) (eq? v v))`, `#t`}, {`(eq? #(1) #(1))`, `#f`}, {`(eq? (vector) (vector))`, `#f`},
		{`(let ((v (vector 1))) (let ((w v)) (vector-set! w 0 2) (vector-ref v 0)))`, `2`},
		{`(memq '(1) '((1)))`, `#f`}, {`(pair? (member "car" (primitives)))`, `#t`},
		{`(assoc-ref (primitive-info "cons") 'origin)`, `"builtin"`},
		{`(assoc-ref (primitive-info "load") 'host)`, `#t`},
		{`(caddr (assoc 'params (primitive-info "cons")))`, `("d" "any" "")`},
		// §6 prelude
		{`(assoc-ref '(("a" . 1)) "a")`, `1`}, {`(assoc-ref '() "a")`, `#<void>`}, {`(assoc-ref '() "a" 'dflt)`, `dflt`},
		{`(identity 7)`, `7`}, {`((compose car cdr) '(1 2))`, `2`}, {`(string-empty? "")`, `#t`}, {`(string-empty? "a")`, `#f`},
	}
	for _, tc := range cases {
		in := New()
		v, err := in.EvalString(context.Background(), tc.src)
		if err != nil {
			t.Errorf("%s\n  error: %v", tc.src, err)
			continue
		}
		if got := Write(v); got != tc.want {
			t.Errorf("%s\n  got  %s\n  want %s", tc.src, got, tc.want)
		}
	}
}

// TestErrors covers §4: every failure is an *Error with a "who: what" text.
func TestErrors(t *testing.T) {
	cases := []struct{ src, want string }{
		{`undefined-var`, "unbound variable: undefined-var"},
		{`()`, "empty application"},
		{`(1 2)`, "not a procedure: 1"},
		{`(car 1)`, "car: argument 1: expected pair"},
		{`(car)`, "car: expected 1 argument(s), got 0"},
		{`(cons 1)`, "cons: expected 2 argument(s), got 1"},
		{`(- )`, "-: expected at least 1 argument(s), got 0"},
		{`((lambda (a) a))`, "#<procedure>: expected 1 argument(s), got 0"},
		{`(define (f a) a) (f 1 2)`, "f: expected 1 argument(s), got 2"},
		{`(set! nope 1)`, "set!: unbound variable: nope"},
		{`(/ 1 0)`, "division by zero"},
		{`(quotient 1 0)`, "division by zero"},
		{`(error "boom" 1 "two")`, `boom 1 "two"`},
		{`(list-ref '(1) 5)`, "out of range"},
		{`(substring "abc" 2 1)`, "out of bounds"},
		{`(string-split "a" "")`, "must not be empty"},
		{`(format "~a")`, "too few arguments"},
		{`(format "x" 1)`, "too many arguments"},
		{`(format "~q" 1)`, "unknown directive"},
		{`(json-parse "{")`, "json-parse:"},
		{`(json-parse "1 2")`, "trailing data"},
		{`(json-emit car)`, "cannot emit"},
		{`(json-emit 'foo)`, "cannot emit symbol"},
		{`(even? 1.0)`, "expected integer"},
		{"`(1 `(2))", "nested quasiquote"},
		{`,x`, "outside quasiquote"},
		{`(else 1)`, "outside cond"},
		{`(if)`, "expected 2 or 3"},
		{`(when)`, "when: expected a test"},
		{`(unless)`, "unless: expected a test"},
		{`(guard)`, "guard: expected"},
		{`(guard (e (#f 1)) (error "unhandled" 7))`, "unhandled 7"},
		{`(guard e (error "x"))`, "expected (var clause"},
		{`(let ((x)) x)`, "bad binding"},
		{`(lambda)`, "expected parameters"},
		{`(define)`, "define: expected"},
		{`(register-tool "cons" car '((doc . "x") (params . ())))`, "already registered"},
		{`(register-tool "t" car '((params . ())))`, "doc and params are required"},
		{`(register-tool "t" car '((doc . "d") (params . (("a" "blob" "x")))))`, "unknown param type"},
		{`(primitive-info "nope")`, "no primitive named"},
		{`(load "/definitely/not/here.scm")`, "load:"},
	}
	for _, tc := range cases {
		in := New()
		_, err := in.EvalString(context.Background(), tc.src)
		if err == nil {
			t.Errorf("%s: expected error containing %q, got none", tc.src, tc.want)
			continue
		}
		var se *Error
		if !errors.As(err, &se) {
			t.Errorf("%s: error is %T, want *Error", tc.src, err)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s\n  got  %q\n  want substring %q", tc.src, err.Error(), tc.want)
		}
	}
}

func TestReaderErrors(t *testing.T) {
	for _, src := range []string{"(", "\"abc", "(1 . ", "'", "#| never closed", "(1 #| open"} {
		if _, err := Read(src); !errors.Is(err, ErrIncomplete) {
			t.Errorf("%q: want ErrIncomplete, got %v", src, err)
		}
	}
	for src, want := range map[string]string{
		")":         "unexpected )",
		"[1]":       "not valid syntax",
		"( . 1)":    "dot without",
		"(1 . 2 3)": "expected ) after dotted tail",
		"#x":        "unknown # syntax",
		"\"\\q\"":   "unknown escape",
	} {
		_, err := Read(src)
		var pe *ParseError
		if !errors.As(err, &pe) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want ParseError containing %q", src, err, want)
		}
	}
	if _, err := Read("(1)\n)"); err == nil || !strings.HasPrefix(err.Error(), "2:1:") {
		t.Errorf("position tracking: %v", err)
	}
}

// TestTailCalls: 20k iterations of a tail-recursive loop, in every tail
// position the doc lists, under an 8 MB Go stack cap — any recursion in a
// tail position would overflow it (a fatal, unmistakable failure). A loop
// whose call is not in a tail position overflows it before 5k iterations,
// with or without -race (2026-10-03); 200k took 18 s under -race.
func TestTailCalls(t *testing.T) {
	old := debug.SetMaxStack(8 << 20)
	defer debug.SetMaxStack(old)
	cases := []string{
		`(define (loop n) (if (= n 0) 'done (loop (- n 1)))) (loop 20000)`,
		`(define (loop n) (cond ((= n 0) 'done) (else (loop (- n 1))))) (loop 20000)`,
		`(define (loop n) (and #t (if (= n 0) 'done (loop (- n 1))))) (loop 20000)`,
		`(define (loop n) (or #f (if (= n 0) 'done (loop (- n 1))))) (loop 20000)`,
		`(define (loop n) (let ((m (- n 1))) (if (< m 0) 'done (loop m)))) (loop 20000)`,
		`(define (loop n) (let* ((m (- n 1))) (if (< m 0) 'done (loop m)))) (loop 20000)`,
		`(define (loop n) (begin 0 (if (= n 0) 'done (loop (- n 1))))) (loop 20000)`,
		`(let loop ((n 20000)) (if (= n 0) 'done (loop (- n 1))))`,
		`(define (even2? n) (if (= n 0) #t (odd2? (- n 1)))) (define (odd2? n) (if (= n 0) #f (even2? (- n 1)))) (if (even2? 20000) 'done 'wrong)`,
	}
	for _, src := range cases {
		in := New()
		if got := Write(eval(t, in, src)); got != "done" {
			t.Errorf("%s => %s", src, got)
		}
	}
}

// TestKernelFixed: loading Scheme that defines things and registers tools
// grows the primitive table and the environment, and nothing else.
func TestKernelFixed(t *testing.T) {
	in := New()
	before := in.Fingerprint()
	nprims := len(in.Prims())
	eval(t, in, `
		(define (twice f x) (f (f x)))
		(define define-syntax 1) ; a binding, not a new form
		(register-tool "double" (lambda (x) (* 2 x))
		  '((doc . "Double a number") (params . (("x" "number" "the number")))))
		(register-tool "shout" (lambda (s . more) (string-upcase s))
		  '((doc . "Upper-case") (params . (("s" "string" "text"))) (variadic . #t)))
	`)
	if in.Fingerprint() != before {
		t.Fatalf("fingerprint changed: %s -> %s", before, in.Fingerprint())
	}
	if got := len(in.Prims()); got != nprims+2 {
		t.Fatalf("prim table: want %d, got %d", nprims+2, got)
	}
	if got := Write(eval(t, in, `(twice double 3)`)); got != "12" {
		t.Fatalf("registered tool through apply: %s", got)
	}
	if got := Write(eval(t, in, `(shout "hi" 1 2)`)); got != `"HI"` {
		t.Fatalf("variadic tool: %s", got)
	}
	if _, err := in.EvalString(context.Background(), `(double 1 2)`); err == nil || !strings.Contains(err.Error(), "double: expected 1 argument(s), got 2") {
		t.Fatalf("arity check on registered tool: %v", err)
	}
	if got := Write(eval(t, in, `(assoc-ref (primitive-info "double") 'origin)`)); got != `"scheme"` {
		t.Fatalf("origin: %s", got)
	}
	if New().Fingerprint() != before {
		t.Fatal("fingerprint differs between fresh interpreters")
	}
}

type recordingGate struct {
	calls []string
	deny  string
}

func (g *recordingGate) Before(_ context.Context, p *Prim, args []Value) error {
	g.calls = append(g.calls, p.Name)
	if p.Name == g.deny {
		return &Denied{Prim: p.Name, Reason: "policy says no"}
	}
	return nil
}

func (g *recordingGate) RegisterChecker(name, verdict string, predicate Value) error {
	return errors.New("recording gate: no checkers")
}

// TestGateIsTheSingleExit: every primitive application — builtin, from a
// registered tool, from map, from apply — crosses the gate, and a denial
// surfaces as a Scheme error the program cannot swallow.
func TestGateIsTheSingleExit(t *testing.T) {
	in := New()
	g := &recordingGate{}
	in.SetGate(g)
	eval(t, in, `
		(register-tool "add1" (lambda (x) (+ x 1)) '((doc . "add one") (params . (("x" "number" "n")))))
		(map add1 '(1 2))
		(apply add1 '(5))
		(sort '(2 1) <)
	`)
	want := []string{"register-tool", "map", "add1", "+", "add1", "+", "apply", "add1", "+", "sort", "<"}
	if strings.Join(g.calls, " ") != strings.Join(want, " ") {
		t.Fatalf("gate saw: %v\nwant:     %v", g.calls, want)
	}

	g.deny = "+"
	_, err := in.EvalString(context.Background(), `(list 1 (+ 1 1))`)
	if err == nil || err.Error() != "+: denied: policy says no" {
		t.Fatalf("denial: %v", err)
	}
	var d *Denied
	if !errors.As(err, &d) || d.Prim != "+" {
		t.Fatalf("denial not preserved as *Denied: %v", err)
	}
}

// TestGuardCannotCatchDenialOrCancellation: a Scheme program must not be
// able to hide a guard refusal or defeat an abort.
func TestGuardCannotCatchDenialOrCancellation(t *testing.T) {
	in := New()
	g := &recordingGate{deny: "+"}
	in.SetGate(g)
	_, err := in.EvalString(context.Background(), `(guard (e (#t 'swallowed)) (+ 1 1))`)
	var d *Denied
	if !errors.As(err, &d) {
		t.Fatalf("denial was caught: %v", err)
	}
	in2 := New()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = in2.EvalString(ctx, `(define (forever n) (forever (+ n 1))) (guard (e (#t 'swallowed)) (forever 0))`)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation was caught: %v", err)
	}
}

func TestCancellation(t *testing.T) {
	in := New()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := in.EvalString(ctx, `(define (forever n) (forever (+ n 1))) (forever 0)`)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation took too long")
	}
}

func TestOutputGoesToHostWriter(t *testing.T) {
	var buf bytes.Buffer
	in := New(WithOutput(&buf))
	eval(t, in, `(display "a") (write "b") (newline) (display '(1 "x"))`)
	if got := buf.String(); got != "a\"b\"\n(1 x)" {
		t.Fatalf("output: %q", got)
	}
	// Default output is discarded, not stdout (ADR 10).
	if New().Output() != (New()).Output() {
		t.Fatal("default output should be the shared discard writer")
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lib.scm")
	os.WriteFile(p, []byte(`(define (lib-fn) 'loaded) (register-tool "lib-tool" lib-fn '((doc . "d") (params . ())))`), 0o600)
	in := New()
	eval(t, in, `(load "`+p+`")`)
	if got := Write(eval(t, in, `(list (lib-fn) (lib-tool))`)); got != "(loaded loaded)" {
		t.Fatalf("load: %s", got)
	}
	bad := filepath.Join(dir, "bad.scm")
	os.WriteFile(bad, []byte(`(car 1)`), 0o600)
	if _, err := in.EvalString(context.Background(), `(load "`+bad+`")`); err == nil || !strings.Contains(err.Error(), "bad.scm: car:") {
		t.Fatalf("load error should name the file: %v", err)
	}
}

func TestDefineNamesClosuresForErrors(t *testing.T) {
	in := New()
	_, err := in.EvalString(context.Background(), `(define f (lambda (a) a)) (f)`)
	if err == nil || !strings.HasPrefix(err.Error(), "f: expected 1") {
		t.Fatalf("closure name in error: %v", err)
	}
}

func TestHandlePrintsOpaque(t *testing.T) {
	h := Value{Kind: KHandle, H: &Handle{Class: "jev-key", ID: "a1b2"}}
	if Write(h) != "#<cred:jev-key:a1b2>" || Display(h) != "#<cred:jev-key:a1b2>" {
		t.Fatalf("handle rendering: %s / %s", Write(h), Display(h))
	}
	in := New()
	in.Global().Define("h", h)
	for _, src := range []string{`(string-append h)`, `(symbol->string h)`, `(json-emit h)`, `(display h)`} {
		_, err := in.EvalString(context.Background(), src)
		if src == `(display h)` {
			continue // prints the opaque form, which is fine
		}
		if err == nil {
			t.Errorf("%s: a builtin turned a handle into text", src)
		}
	}
	if got := Write(eval(t, in, `(handle? h)`)); got != "#t" {
		t.Fatal("handle? on a handle")
	}
	if !h.Taint.IsClean() {
		t.Fatal("zero taint must be clean")
	}
}

// TestDocCoverage enforces ADR 7: every primitive is documented in
// docs/scheme.md.
func TestDocCoverage(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "scheme.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	for _, p := range New().Prims() {
		if !strings.Contains(text, "`("+p.Name+" ") && !strings.Contains(text, "`("+p.Name+")") && !strings.Contains(text, "`"+p.Name+"`") {
			t.Errorf("primitive %q is not documented in docs/scheme.md", p.Name)
		}
		if p.Meta.Doc == "" {
			t.Errorf("primitive %q has no doc string", p.Name)
		}
	}
	for _, sf := range specialForms {
		if !strings.Contains(text, "`("+sf+" ") && !strings.Contains(text, "`"+sf+"`") && !strings.Contains(text, "("+sf+" ") {
			t.Errorf("special form %q is not documented in docs/scheme.md", sf)
		}
	}
}

func FuzzReader(f *testing.F) {
	for _, s := range []string{"(+ 1 2)", "'(a . b)", "\"str\\n\"", "`(1 ,x ,@y)", "#t", "1.5e3", "(((", ")", "; c\n1", "#(1 2 \"x\")", "#( )", "#| c |# 1", "#(#(1) 2)"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		vs, err := Read(src)
		if err != nil {
			return
		}
		// Whatever reads must print and read back to an equal datum.
		for _, v := range vs {
			back, err := ReadOne(Write(v))
			if err != nil {
				t.Fatalf("%q: reprint %q does not read: %v", src, Write(v), err)
			}
			if !Equal(v, back) {
				t.Fatalf("%q: round trip %s != %s", src, Write(v), Write(back))
			}
		}
	})
}

// Raw strings exist so prose can live in a program: instructions, tool
// documentation, templates. No escapes, and an indented block reads at the
// indentation it is written at.
func TestRawStrings(t *testing.T) {
	cases := []struct{ src, want string }{
		// Verbatim when the opening delimiter is not followed by a newline.
		{`"""one line"""`, `"one line"`},
		{`"""has "quotes" and \n backslashes"""`, `"has \"quotes\" and \\n backslashes"`},
		{`""""""`, `""`},
		// An indented block loses the first newline, the shared indentation
		// and the newline before the closing delimiter.
		{"\"\"\"\n  alpha\n  beta\n  \"\"\"", `"alpha\nbeta"`},
		// Deeper lines keep their relative indentation; blank lines survive.
		{"\"\"\"\n  alpha\n    deeper\n\n  beta\n  \"\"\"", `"alpha\n  deeper\n\nbeta"`},
		// The closing delimiter's own indentation sets the margin even when
		// a line is flush left.
		{"\"\"\"\nflush\n  indented\n\"\"\"", `"flush\n  indented"`},
		// A raw string is an ordinary string everywhere else.
		{`(string-length """abc""")`, `3`},
		{`(string-append """a""" "b")`, `"ab"`},
	}
	for _, tc := range cases {
		in := New()
		v, err := in.EvalString(context.Background(), tc.src)
		if err != nil {
			t.Errorf("%s: %v", tc.src, err)
			continue
		}
		if got := Write(v); got != tc.want {
			t.Errorf("%s\n  got  %s\n  want %s", tc.src, got, tc.want)
		}
	}
	// An unterminated block is incomplete input, so the REPL keeps reading.
	if _, err := Read(`"""never closed`); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("unterminated raw string: %v", err)
	}
	// Round trip: what a raw string produces, write renders readably.
	v, err := New().EvalString(context.Background(), "\"\"\"\n  a \"b\"\n  \"\"\"")
	if err != nil {
		t.Fatal(err)
	}
	back, err := ReadOne(Write(v))
	if err != nil || !Equal(v, back) {
		t.Fatalf("round trip: %v %s", err, Write(v))
	}
}

// TestRunawayRecursionIsAnError: a recursion that never ends, a value that
// holds itself or is nested past MaxDepth, a file that loads itself and a datum nested past MaxDepth are
// each an error the program can catch, and the interpreter goes on working.
// Without the limit each one is a Go stack overflow, which ends the process:
// the daemon and every session in it. The stack cap makes that a quick,
// unmistakable failure here; MaxDepth's nesting fits well inside it.
func TestRunawayRecursionIsAnError(t *testing.T) {
	old := debug.SetMaxStack(256 << 20)
	defer debug.SetMaxStack(old)
	in := New()
	eval(t, in, `(define (runaway n) (+ 1 (runaway n)))`)
	for _, src := range []string{
		`(runaway 1)`,
		`(define (through-map n) (map (lambda (x) (through-map x)) '(1))) (through-map 1)`,
	} {
		if _, err := in.EvalString(context.Background(), src); err == nil || !strings.Contains(err.Error(), "recursion too deep") {
			t.Fatalf("%s: %v", src, err)
		}
	}
	if got := Write(eval(t, in, `(guard (e (#t 'caught)) (runaway 1))`)); got != "caught" {
		t.Fatalf("guard should catch the recursion error, got %s", got)
	}
	// The count of nested evaluations is back to zero: a legal recursion
	// almost as deep as the limit still runs.
	if got := Write(eval(t, in, `(define (down n) (if (= n 0) 0 (+ 1 (down (- n 1))))) (down 9000)`)); got != "9000" {
		t.Fatalf("a recursion inside the limit: %s", got)
	}

	eval(t, in, `(define a (vector 0)) (vector-set! a 0 a) (define b (vector 0)) (vector-set! b 0 b)`)
	if got := Write(eval(t, in, `(list (equal? a a) (equal? a b))`)); got != "(#t #f)" {
		t.Fatalf("equal? on vectors that hold themselves: %s", got)
	}
	if w := Write(eval(t, in, `a`)); !strings.HasPrefix(w, "#(#(#(") || !strings.Contains(w, "#(...)") {
		t.Fatal("a vector that holds itself should print cut short")
	}
	eval(t, in, `(define deep (let loop ((n 20000) (acc '())) (if (= n 0) acc (loop (- n 1) (list acc)))))`)
	if _, err := in.EvalString(context.Background(), `(json-emit deep)`); err == nil || !strings.Contains(err.Error(), "nested more than") {
		t.Fatalf("json-emit of a list nested 20000 deep: %v", err)
	}
	if got := Write(eval(t, in, `(equal? deep deep)`)); got != "#t" {
		t.Fatalf("equal? of a deep list with itself: %s", got)
	}

	self := filepath.Join(t.TempDir(), "self.scm")
	if err := os.WriteFile(self, []byte(`(load "`+self+`")`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := in.EvalString(context.Background(), `(load "`+self+`")`); err == nil || !strings.Contains(err.Error(), "loaded again") {
		t.Fatalf("a file that loads itself: %v", err)
	}
	if _, err := Read(strings.Repeat("(", 200_000) + strings.Repeat(")", 200_000)); err == nil || !strings.Contains(err.Error(), "nested more than") {
		t.Fatalf("a datum nested past the limit: %v", err)
	}
	if got := Write(eval(t, in, `(+ 1 2)`)); got != "3" {
		t.Fatalf("the interpreter should go on working: %s", got)
	}
}

// A primitive's own deadline, under a turn that goes on, is that call
// failing, and guard catches it, as the plan gate's judge fallback needs;
// the turn's own cancellation still passes through guard.
func TestGuardCatchesAPrimitivesOwnTimeout(t *testing.T) {
	in := New()
	if err := in.Register(&Prim{Name: "slow-judge", Meta: Meta{Doc: "d", Host: true}, Handler: func(c *Call) (Value, error) {
		return Value{}, fmt.Errorf("judge: %w", context.DeadlineExceeded)
	}}); err != nil {
		t.Fatal(err)
	}
	if got := Write(eval(t, in, `(guard (e (#t 'planned-anyway)) (slow-judge))`)); got != "planned-anyway" {
		t.Fatalf("a primitive's own timeout: %s", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := in.EvalString(ctx, `(guard (e (#t 'caught)) (slow-judge))`); err == nil {
		t.Fatal("guard caught a timeout after the turn was cancelled")
	}
}
