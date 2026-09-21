package prim

import (
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	a := "one\ntwo\nthree\nfour\nfive\nsix\nseven\n"
	b := "one\ntwo\n3\nfour\nfive\nsix\nseven\neight\n"
	got := Unified("f", a, b, 1)
	want := "--- f\n+++ f\n@@ -2,3 +2,3 @@\n two\n-three\n+3\n four\n@@ -7,1 +7,2 @@\n seven\n+eight\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if d := Unified("f", "same\n", "same\n", 3); d != "--- f\n+++ f\n" {
		t.Fatalf("identical: %q", d)
	}
	if d := Unified("f", "", "new\nfile\n", 3); !strings.Contains(d, "+new\n+file\n") {
		t.Fatalf("new file: %q", d)
	}
	if d := Unified("f", "gone\n", "", 3); !strings.Contains(d, "-gone\n") {
		t.Fatalf("deleted: %q", d)
	}
}

// Every edit script must transform a into b.
func TestMyersEditScriptIsCorrect(t *testing.T) {
	cases := [][2]string{
		{"a b c d e f", "a x c d y f g"},
		{"", "a"}, {"a", ""}, {"a b", "b a"}, {"x x x", "x x"},
		{"1 2 3 4 5 6 7 8 9", "9 8 7 6 5 4 3 2 1"},
	}
	for _, c := range cases {
		a, b := strings.Fields(c[0]), strings.Fields(c[1])
		var rebuilt []string
		ai := 0
		for _, o := range myers(a, b) {
			switch o.kind {
			case ' ':
				if a[ai] != o.line {
					t.Fatalf("%v: equal op mismatch", c)
				}
				rebuilt = append(rebuilt, o.line)
				ai++
			case '-':
				ai++
			case '+':
				rebuilt = append(rebuilt, o.line)
			}
		}
		if strings.Join(rebuilt, " ") != c[1] || ai != len(a) {
			t.Fatalf("%v: script does not produce b: %v", c, rebuilt)
		}
	}
}
