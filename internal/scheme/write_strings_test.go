package scheme

import (
	"strings"
	"testing"
)

func TestWriteWithStrings(t *testing.T) {
	value := Cons(Str("before\nafter"), Vec(Str("tab\tvalue"), Int(7)))
	original := Write(value)
	var visited []string
	got := WriteWithStrings(value, func(s string) string {
		visited = append(visited, s)
		return strings.ReplaceAll(strings.ReplaceAll(s, "\n", "<newline>"), "\t", "<tab>")
	})
	if got != `("before<newline>after" . #("tab<tab>value" 7))` {
		t.Fatal(got)
	}
	if len(visited) != 2 || visited[0] != "before\nafter" || visited[1] != "tab\tvalue" {
		t.Fatal("transform did not receive decoded leaves", visited)
	}
	if Write(value) != original || WriteWithStrings(value, nil) != original {
		t.Fatal("rendering changed original value or default output")
	}
}
