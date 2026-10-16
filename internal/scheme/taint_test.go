package scheme

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

func tainted(t *testing.T, in *Interp, src string) Value {
	t.Helper()
	v, err := in.EvalString(WithOrigin(context.Background(), OriginModel), src)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return v
}

// withSource registers a primitive that mints a tagged value, standing in
// for a host primitive.
func withSource(t *testing.T) *Interp {
	t.Helper()
	in := New()
	if err := in.Register(&Prim{
		Name: "read-thing",
		Meta: Meta{Doc: "d", Host: true, TaintSource: TagFile,
			Params: []Param{{Name: "path", Type: "string"}}},
		Handler: func(c *Call) (Value, error) { return Str("contents of " + c.Args[0].S), nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := in.Register(&Prim{
		Name:    "list-thing",
		Meta:    Meta{Doc: "d", Host: true, TaintSource: TagFile},
		Handler: func(c *Call) (Value, error) { return List(Str("a"), Str("b")), nil },
	}); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestSourceTagsAndPropagation(t *testing.T) {
	in := withSource(t)
	if got := tainted(t, in, `(read-thing "x")`).Taint.String(); got != TagFile {
		t.Fatalf("source: %q", got)
	}
	if !tainted(t, in, `"plain"`).Taint.IsClean() {
		t.Fatal("a literal must be clean")
	}
	// Propagation through composition, through map, through a closure.
	for _, src := range []string{
		`(string-append "k=" (read-thing "x"))`,
		`(car (list (read-thing "x") "b"))`,
		`(string-upcase (substring (read-thing "x") 0 3))`,
		`(car (map string-upcase (list (read-thing "x"))))`,
		`((lambda (v) (string-append v v)) (read-thing "x"))`,
		`(cdr (cons "a" (read-thing "x")))`,
		`(json-emit (list (cons "k" (read-thing "x"))))`,
	} {
		if got := tainted(t, in, src).Taint.String(); got != TagFile {
			t.Errorf("%s: taint %q, want %q", src, got, TagFile)
		}
	}
	// Opaque primitives measure, they do not carry.
	for _, src := range []string{
		`(string-length (read-thing "x"))`,
		`(string=? (read-thing "x") "y")`,
		`(null? (list-thing))`,
		`(length (list-thing))`,
		`(vector-length (list->vector (list-thing)))`,
		`(string-prefix? "c" (read-thing "x"))`,
		`(> (string->number (string-append "1" (substring (read-thing "x") 0 0))) 0)`,
		`(positive? (string->number (string-append "1" (substring (read-thing "x") 0 0))))`,
		`(odd? (string->number (string-append "1" (substring (read-thing "x") 0 0))))`,
		`(number? (read-thing "x"))`,
	} {
		if !tainted(t, in, src).Taint.IsClean() {
			t.Errorf("%s: should be opaque", src)
		}
	}
}

// A string made from a list or a vector takes the taint of what it holds,
// however the list was built: map, quasiquote and vector-set! build them
// without unioning their elements' taint into the container. So do the
// output with-output-to-string captures and the message guard hands on from
// an error that quoted a tainted value. Each of these was clean before.
func TestTaintReachesAStringMadeFromAContainer(t *testing.T) {
	in := withSource(t)
	for _, src := range []string{
		`(string-join (map (lambda (p) (read-thing p)) (list "a" "b")) "\n")`,
		`(json-emit (map read-thing (list "a")))`,
		`(let ((x (read-thing "a"))) (json-emit ` + "`" + `(("k" . ,x))))`,
		`(let ((v (vector "a"))) (vector-set! v 0 (read-thing "a")) (string-join (vector->list v) ","))`,
		`(with-output-to-string (display (read-thing "a")))`,
		`(with-output-to-string (write (list (read-thing "a"))))`,
		`(guard (e (#t (assoc-ref e 'message))) (+ 1 (read-thing "a")))`,
	} {
		if got := tainted(t, in, src).Taint.String(); got != TagFile {
			t.Errorf("%s: taint %q, want %q", src, got, TagFile)
		}
	}
}

// TestTaintSupersetProperty is the plan's acceptance property: for random
// expression trees over pure primitives, the result's taint contains the
// union of the tainted leaves'.
func TestTaintSupersetProperty(t *testing.T) {
	in := withSource(t)
	rng := rand.New(rand.NewSource(7))
	var build func(depth int) (src string, leaves int)
	build = func(depth int) (string, int) {
		if depth == 0 {
			if rng.Intn(2) == 0 {
				return `(read-thing "f")`, 1
			}
			return `"clean"`, 0
		}
		a, la := build(depth - 1)
		b, lb := build(depth - 1)
		switch rng.Intn(4) {
		case 0:
			return "(string-append " + a + " " + b + ")", la + lb
		case 1:
			return "(car (list " + a + " " + b + "))", la + lb
		case 2:
			return "(cadr (list " + a + " " + b + "))", la + lb
		default:
			return "(string-join (list " + a + " " + b + ") \"-\")", la + lb
		}
	}
	for i := 0; i < 200; i++ {
		src, leaves := build(3)
		v := tainted(t, in, src)
		if leaves > 0 && !v.Taint.Has(TagFile) {
			t.Fatalf("lost taint: %s", src)
		}
		if leaves == 0 && !v.Taint.IsClean() {
			t.Fatalf("invented taint: %s", src)
		}
	}
}

func TestHandlesCarryTheirClass(t *testing.T) {
	in := withSource(t)
	in.Global().Define("h", HandleValue(&Handle{Class: "jev-key", ID: "a1"}))
	if got := tainted(t, in, `h`).Taint.String(); got != "cred:jev-key" {
		t.Fatalf("handle taint: %q", got)
	}
	if got := tainted(t, in, `(taint-of h)`); Write(got) != `("cred:jev-key")` {
		t.Fatalf("taint-of: %s", Write(got))
	}
	if got := tainted(t, in, `(list (tainted? h) (tainted? "x"))`); Write(got) != "(#t #f)" {
		t.Fatalf("tainted?: %s", Write(got))
	}
}

func TestWipeStripsAndIsUserOnlyAtTheGuard(t *testing.T) {
	in := withSource(t)
	// Without a gate installed, wipe simply strips.
	v, err := in.EvalString(WithOrigin(context.Background(), OriginUser), `(wipe (read-thing "x"))`)
	if err != nil || !v.Taint.IsClean() {
		t.Fatalf("wipe: %v %q", err, v.Taint)
	}
	if v.S != "contents of x" {
		t.Fatalf("wipe must not change the value: %q", v.S)
	}
	// wipe is never offered to the model as a tool, and box, its old
	// synonym, no longer exists.
	for _, p := range in.Tools() {
		if p.Name == "wipe" {
			t.Fatalf("%s is exposed to the model", p.Name)
		}
	}
	if _, ok := in.Lookup("box"); ok {
		t.Fatal("box is still a primitive")
	}
}

func TestTagNames(t *testing.T) {
	a := TaintOf("external:file", "cred:x")
	if got := a.String(); got != "cred:x,external:file" {
		t.Fatalf("tags: %q", got)
	}
	if got := strings.Join(a.Creds(), ","); got != "x" {
		t.Fatalf("creds: %q", got)
	}
	if got := strings.Join(a.Externals(), ","); got != "external:file" {
		t.Fatalf("externals: %q", got)
	}
	if Union(TaintOf("a"), TaintOf("b")).Tags() == nil {
		t.Fatal("union lost tags")
	}
}

func TestTagSetsPreserveExactNamesBeyond63(t *testing.T) {
	var names []string
	for i := 0; i < 200; i++ {
		names = append(names, fmt.Sprintf("external:source-%03d", i), CredTag(fmt.Sprintf("service-%03d", i)))
	}
	all := TaintOf(names...)
	if len(all.Tags()) != 400 || len(all.Creds()) != 200 || len(all.Externals()) != 200 {
		t.Fatalf("lost attribution: %d tags, %d credentials, %d sources", len(all.Tags()), len(all.Creds()), len(all.Externals()))
	}
	for _, name := range names {
		if !all.Has(name) {
			t.Errorf("lost %q", name)
		}
	}
	if all.Has("unknown") || all.Has("tainted:unattributed") {
		t.Fatal("a missing tag must not alias another tag")
	}
	// Creating other sets cannot change existing attribution.
	for i := 0; i < 200; i++ {
		_ = TaintOf(fmt.Sprintf("unrelated:%d", i))
	}
	if !TaintOf(names[len(names)-1]).Has(names[len(names)-1]) {
		t.Fatal("tag identity depends on prior sets")
	}
}

func TestTagSetsAreImmutable(t *testing.T) {
	input := []string{"z", "a", "a"}
	original := TaintOf(input...)
	if !slices.Equal(input, []string{"z", "a", "a"}) {
		t.Fatal("constructor mutated input")
	}
	input[0] = "changed"
	output := original.Tags()
	if !slices.Equal(output, []string{"a", "z"}) {
		t.Fatalf("tags: %v", output)
	}
	output[0] = "changed"
	combined := Union(original, TaintOf("b", "a"))
	if !slices.Equal(combined.Tags(), []string{"a", "b", "z"}) {
		t.Fatalf("union: %v", combined.Tags())
	}
	if !slices.Equal(original.Tags(), []string{"a", "z"}) {
		t.Fatal("original was mutated")
	}
	if !Union(Taint{}, TaintOf()).IsClean() {
		t.Fatal("zero sets should be clean")
	}
	if !slices.Equal(Union(original, original).Tags(), original.Tags()) {
		t.Fatal("union duplicated tags")
	}
	// Value copies may share the immutable representation safely.
	before := Tainted(Str("x"), original)
	after := Tainted(before, TaintOf("new"))
	if before.Taint.Has("new") || !after.Taint.Has("new") {
		t.Fatal("value copy lost isolation")
	}
}
