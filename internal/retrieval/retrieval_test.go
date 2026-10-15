package retrieval

import (
	"slices"
	"testing"
)

func TestWordsSplitNamesAsCodeWritesThem(t *testing.T) {
	for text, want := range map[string][]string{
		"ScrubEnv":                 {"scrubenv", "scrub", "env"},
		"index-grep":               {"index-grep", "index", "grep"},
		"max_result_tokens":        {"max_result_tokens", "max", "result", "tokens"},
		"HTTPServer":               {"httpserver", "http", "server"},
		"parseHTTPHeader2":         {"parsehttpheader2", "parse", "http", "header"},
		"--force a, B; 812":        {"force", "812"},
		"élan Ñandú":               {"élan", "ñandú"},
		"go test ./internal/core/": {"go", "test", "internal", "core"},
	} {
		if got := Words(text); !slices.Equal(got, want) {
			t.Errorf("Words(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestSearchRanksRareWordsAndWeightedFields(t *testing.T) {
	var x Index
	plain := x.Add(Field{Text: "shell-exec go build"}, Field{Text: "test ok"})
	rare := x.Add(Field{Text: "the binding check refused the key"})
	called := x.Add(Field{Text: "shell-exec go test", Weight: 3}, Field{Text: "build ok"})
	x.Add(Field{Text: "nothing of interest here"})
	if got := x.Search("binding test", 10); len(got) != 3 || got[0].Doc != rare {
		t.Fatalf("a word one document holds outranks one several hold: %+v", got)
	}
	got := x.Search("test", 10)
	if len(got) != 2 || got[0].Doc != called || got[1].Doc != plain {
		t.Fatalf("a word in a weighted field counts more: %+v", got)
	}
	if !slices.Equal(got[0].Matched, []string{"test"}) {
		t.Fatalf("matched words: %q", got[0].Matched)
	}
	if got := x.Search("binding binding", 10); len(got) != 1 || len(got[0].Matched) != 1 {
		t.Fatalf("a repeated query word counts once: %+v", got)
	}
	if got := x.Search("test", 1); len(got) != 1 {
		t.Fatalf("limit: %+v", got)
	}
	if got := x.Search("absent", 10); len(got) != 0 {
		t.Fatalf("no match: %+v", got)
	}
	var empty Index
	if got := empty.Search("test", 10); got != nil {
		t.Fatalf("empty index: %+v", got)
	}
}

func TestSearchBreaksTiesInTheOrderAdded(t *testing.T) {
	var x Index
	for range 3 {
		x.Add(Field{Text: "same words"})
	}
	got := x.Search("same", 10)
	if len(got) != 3 || got[0].Doc != 0 || got[1].Doc != 1 || got[2].Doc != 2 {
		t.Fatalf("ties: %+v", got)
	}
}
