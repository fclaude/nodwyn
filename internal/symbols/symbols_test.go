package symbols

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zema1/wasitter"
)

func TestOutlines(t *testing.T) {
	for _, tt := range []struct {
		lang, source string
		names        []string
	}{
		{"go", "package p\ntype Thing struct{}\nfunc Start() {}\nfunc (t Thing) Run() {}\n", []string{"Thing", "Start", "Run"}},
		{"javascript", "// function fake() {}\nclass Box { run() {} }\nfunction start() { function nested() {} }\n", []string{"Box", "run", "start", "nested"}},
		{"python", "# def fake(): pass\nclass Box:\n    def run(self):\n        pass\ndef start():\n    pass\n", []string{"Box", "run", "start"}},
	} {
		t.Run(tt.lang, func(t *testing.T) {
			out, err := Parse(context.Background(), tt.lang, []byte(tt.source))
			if err != nil {
				t.Fatal(err)
			}
			if out.ParseErrors || out.Truncated || len(out.Definitions) != len(tt.names) {
				t.Fatalf("%+v", out)
			}
			for i, name := range tt.names {
				if out.Definitions[i].Name != name || out.Definitions[i].Line < 1 || out.Definitions[i].EndLine < out.Definitions[i].Line {
					t.Fatalf("%+v", out)
				}
			}
		})
	}
}

func TestIncompleteAndBoundedOutlines(t *testing.T) {
	out, err := Parse(context.Background(), "go", []byte("package p\nfunc Good() {}\nfunc Broken(\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !out.ParseErrors || len(out.Definitions) < 1 || out.Definitions[0].Name != "Good" {
		t.Fatalf("%+v", out)
	}
	var source strings.Builder
	source.WriteString("package p\n")
	for i := 0; i < 101; i++ {
		fmt.Fprintf(&source, "func f%d() {}\n", i)
	}
	out, err = Parse(context.Background(), "go", []byte(source.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Definitions) != 100 || !out.Truncated {
		t.Fatalf("%+v", out)
	}
	if _, err := Language("example.scm"); err == nil {
		t.Fatal("unsupported language accepted")
	}
	if _, err := Parse(context.Background(), "not-a-grammar", nil); err == nil {
		t.Fatal("unbundled grammar accepted")
	}
}

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Parse(ctx, "go", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Cancellation interrupts runtime creation or guest execution, and cannot
	// return an apparently complete outline from a partial traversal.
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Parse(ctx, "javascript", []byte(strings.Repeat("function f() {}\n", 10000)))
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatal(err, time.Since(start))
	}
}

func TestPinnedGrammarAssets(t *testing.T) {
	for name, want := range map[string]string{
		"go":         "2812c281eeee4a59ab27ae71595a625c1e50619507431a8f85f03edd361ae508",
		"python":     "0b4396e688a08f9d2c6a11de2b018bdda63eef7b59497ee1fd56481cc96f5423",
		"javascript": "7abe2b54935fc1bf33bdcb8f48434676b801bcf62eca104b19b1dd1af18bfa5b",
	} {
		var raw []byte
		if name == "javascript" {
			raw = wasitter.BuiltinJavaScriptWASM()
		} else {
			var err error
			raw, err = grammars.ReadFile("grammars/wasitter-" + name + ".wasm")
			if err != nil {
				t.Fatal(err)
			}
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != want {
			t.Fatalf("%s: %s", name, got)
		}
	}
}

func TestNamesAreNotFabricatedByTruncation(t *testing.T) {
	out, err := Parse(context.Background(), "go", []byte("package p\nfunc "+strings.Repeat("a", 600)+"() {}\nfunc Kept() {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || len(out.Definitions) != 1 || out.Definitions[0].Name != "Kept" {
		t.Fatalf("%+v", out)
	}
}
