package prim

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/fclaude/brote/scheme"
)

// The support check reads the answer to the question asked as an existing
// test is edited (ADR 32, amended 2026-10-16). Its fixtures are the request
// of three commit-bench tasks, the test files of the patches some attempts
// made, and the replies dsl21 and dsl22 gave to the question on 94f5cac4,
// all in testdata/support.

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "support", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// patchChange reads a unified diff as the change it makes to its test files:
// each file as HEAD had it and as it is after the patch, the lines outside
// the hunks blank in both, so a line keeps its number at HEAD. Files the
// patch adds have no HEAD; files it deletes have no "now".
func patchChange(diff string) changeFiles {
	type file struct {
		head, now     []string
		hasHead, gone bool
		headPos       int
	}
	files := map[string]*file{}
	var order []string
	var cur *file
	hunk := regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+\d+(?:,\d+)? @@`)
	lines := strings.Split(diff, "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "diff --git "):
			parts := strings.Fields(l)
			name := strings.TrimPrefix(parts[len(parts)-1], "b/")
			cur = &file{hasHead: true}
			files[name] = cur
			order = append(order, name)
		case cur != nil && strings.HasPrefix(l, "--- /dev/null"):
			cur.hasHead = false
		case cur != nil && strings.HasPrefix(l, "+++ /dev/null"):
			cur.gone = true
		case cur != nil && strings.HasPrefix(l, "@@"):
			m := hunk.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			start = max(start, 1)
			for cur.headPos < start-1 {
				cur.head, cur.now = append(cur.head, ""), append(cur.now, "")
				cur.headPos++
			}
			j := i + 1
			for ; j < len(lines); j++ {
				b := lines[j]
				if strings.HasPrefix(b, "diff --git ") || strings.HasPrefix(b, "@@") || b == "" && j == len(lines)-1 {
					break
				}
				switch {
				case b == "":
					cur.head, cur.now = append(cur.head, ""), append(cur.now, "")
					cur.headPos++
				case b[0] == ' ':
					cur.head, cur.now = append(cur.head, b[1:]), append(cur.now, b[1:])
					cur.headPos++
				case b[0] == '-':
					cur.head = append(cur.head, b[1:])
					cur.headPos++
				case b[0] == '+':
					cur.now = append(cur.now, b[1:])
				}
			}
			i = j - 1
		}
	}
	var paths []string
	for _, name := range order {
		if expectationTest(name) {
			paths = append(paths, name)
		}
	}
	slices.Sort(paths)
	return changeFiles{
		paths: paths,
		head: func(rel string) ([]byte, bool) {
			f := files[rel]
			if f == nil || !f.hasHead {
				return nil, false
			}
			return []byte(strings.Join(f.head, "\n") + "\n"), true
		},
		now: func(rel string) ([]byte, bool) {
			f := files[rel]
			if f == nil || f.gone {
				return nil, false
			}
			return []byte(strings.Join(f.now, "\n") + "\n"), true
		},
		dir: func(string) []string { return nil },
	}
}

// requestSentences is the sentences of a request, one to a line of a list or
// to a full stop, as a model would quote them.
func requestSentences(request string) []string {
	var out []string
	for _, s := range regexp.MustCompile(`[.!?]\s+|\n\n|\n- |;\s`).Split(request, -1) {
		if s = strings.Join(strings.Fields(s), " "); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// naturalQuote is the sentence of the request a model would most likely
// quote for a row: the one that shares the most tokens with it, the first of
// those that tie.
func naturalQuote(r removedLine, request string) []string {
	tokens := rowTokens(r)
	best, quote := 0, ""
	for _, s := range requestSentences(request) {
		n := 0
		for tok := range supportTokens([]string{s}, nil) {
			if tokens[tok] {
				n++
			}
		}
		if n > best {
			best, quote = n, s
		}
	}
	if quote == "" {
		return nil
	}
	return requestQuotes(request, quote)
}

func rowsOf(t *testing.T, patch string) []removedLine {
	t.Helper()
	return removedExpectations(patchChange(fixture(t, patch)))
}

func statusOf(r removedLine, request string, quotes []string) supportStatus {
	return judgeRow(r, request, quotes).status
}

func rowNamed(t *testing.T, rows []removedLine, text string) removedLine {
	t.Helper()
	for _, r := range rows {
		if strings.Contains(r.text, text) {
			return r
		}
	}
	t.Fatalf("no row with %q among %d", text, len(rows))
	return removedLine{}
}

// A quote is a run of three words or more of the answer that stands in the
// request, in the same order, the case, the punctuation, the white space and
// the quotation marks set aside; a paraphrase is none, nor a run in the wrong
// order, nor two words.
func TestQuotesAreRunsOfTheRequest(t *testing.T) {
	request := fixture(t, "94f5cac4.request.txt")
	for _, c := range []struct {
		name, answer string
		want         []string
	}{
		{"verbatim, in backquotes", "`fs-read's start 0 is line 1` covers it", []string{"fs read s start 0 is line 1"}},
		{"in curly quotes, wrapped in the request", "“index-grep caps context above 5 instead of refusing it (models asked for 6, 8 and 10)” — capped.", []string{"index grep caps context above 5 instead of refusing it models asked for 6 8 and 10"}},
		{"case and white space", "THE  HANDLERS\ntreat as   not given", []string{"the handlers treat as not given"}},
		{"a paraphrase", "a missing start is read as the first line", nil},
		{"the wrong order", "given as not treat handlers", nil},
		{"two words", "empty value", nil},
		{"two runs", "first `count needs start too` and then `glob is every file`", []string{"count needs start too", "glob is every file"}},
		{"the same run twice", "`glob is every file`, `glob is every file`", []string{"glob is every file"}},
	} {
		if got := requestQuotes(request, c.answer); !slices.Equal(got, c.want) {
			t.Errorf("%s: quotes %q, want %q", c.name, got, c.want)
		}
	}
}

// A row is judged by the distinctive words of the string literals it holds:
// its input and the message it expects. Short words, 0 and 1, stop words and
// the parts of a called function's name are no token; a number from 2 up is;
// a row whose literals give none is read by its identifiers.
func TestTheTokensOfARow(t *testing.T) {
	for _, c := range []struct {
		row  removedLine
		want []string
	}{
		{removedLine{path: "a_test.go", text: "`(fs-read \"big.txt\" 1 0)`:  \"count must be at least 1\","}, []string{"big", "count", "least", "txt"}},
		{removedLine{path: "a_test.go", text: "`(index-grep \"pkg\" \"needle\" \"*.py\" 6)`: \"context is 0 to 5 lines\","}, []string{"5", "6", "context", "line", "needle", "pkg"}},
		{removedLine{path: "a_test.go", text: "`{\"path\":\"p\",\"count\":4}`: `argument \"count\" needs \"start\" too`,"}, []string{"4", "argument", "count", "need", "path", "start"}},
		{removedLine{path: "a_test.go", text: "{Role: RoleSystem, Content: \"\"},"}, []string{"content", "role", "system"}},
		{removedLine{path: "a_test.go", text: "{\"\", false, nil},"}, nil},
		// What a rewrite puts in its place counts too.
		{removedLine{path: "a_test.go", text: "known := map[string]bool{\"model\": true, \"input\": true,", repl: "known := map[string]bool{\"model\": true, \"instructions\": true, \"input\": true,"}, []string{"input", "instruction", "model"}},
	} {
		var got []string
		for tok := range rowTokens(c.row) {
			got = append(got, tok)
		}
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: tokens %q, want %q", c.row.text, got, c.want)
		}
	}
}

// The three rows of 94f5cac4 a correct change rewrites are supported by the
// sentences of the request a model quotes for them, and the fourth, which
// the request never mentions, is not: a count of 0 is not "a skipped
// argument". The quotes are those dsl22 gave (R28, R31, R32); R27 quoted
// nothing.
func TestTheRepliesOf94f5cac4AreReadRowByRow(t *testing.T) {
	request := fixture(t, "94f5cac4.request.txt")
	rows := rowsOf(t, "94f5cac4.dsl22.patch")
	if len(rows) != 5 {
		t.Fatalf("%d rows named, want 5: %+v", len(rows), rows)
	}
	replies := map[string]string{}
	for _, part := range strings.Split(fixture(t, "94f5cac4.dsl22.replies.txt"), "#### ")[1:] {
		head, body, _ := strings.Cut(part, "\n")
		replies[head] = body
	}
	read := func(reply string, text string) supportStatus {
		return statusOf(rowNamed(t, rows, text), request, requestQuotes(request, reply))
	}
	for _, c := range []struct {
		reply, row string
		want       supportStatus
	}{
		{"R27", `{"path":"p","count":4}`, unsupported}, // "Restoring the type-check case": no quote
		{"R28", `{"path":"p","count":4}`, supported},
		{"R28", `{"path":"p","start":null,"count":4}`, supported},
		{"R31", `(fs-read "big.txt" 0)`, supported},
		{"R31", `(fs-read "big.txt" 1 0)`, unsupported}, // the rule for a skipped argument says nothing of count 0
		{"R32", `(index-grep "pkg" "needle" "*.py" 6)`, supported},
		{"R32", `(fs-read "big.txt" 1 0)`, unsupported},
	} {
		if got := read(replies[c.reply], c.row); got != c.want {
			t.Errorf("%s on %s: %s, want %s", c.reply, c.row, got, c.want)
		}
	}
	if q := requestQuotes(request, replies["R27"]); len(q) != 0 {
		t.Errorf("R27 quotes %q", q)
	}
	// The firmer message says what the request does not mention.
	j := judgeRow(rowNamed(t, rows, `(fs-read "big.txt" 1 0)`), request, requestQuotes(request, replies["R31"]))
	if want := []string{`(fs-read "big.txt" 1 0)`, "count must be at least 1"}; !slices.Equal(j.unmentioned, want) {
		t.Errorf("unmentioned %q, want %q", j.unmentioned, want)
	}
	// Every row of dsl21's patch is supported by the sentences it quoted.
	rows21 := rowsOf(t, "94f5cac4.dsl21.patch")
	var quotes []string
	for _, part := range strings.Split(fixture(t, "94f5cac4.dsl21.replies.txt"), "#### ")[1:] {
		_, body, _ := strings.Cut(part, "\n")
		quotes = append(quotes, requestQuotes(request, body)...)
	}
	if len(rows21) != 4 {
		t.Fatalf("%d rows in dsl21's patch, want 4: %+v", len(rows21), rows21)
	}
	for _, r := range rows21 {
		if got := statusOf(r, request, quotes); got != supported {
			t.Errorf("dsl21: %s is %s", r.text, got)
		}
	}
}

// A rewrite is paired with the line it replaces by the words they share: the
// most alike first, each line once, so a block that moves several rows pairs
// each with its own, and a line put in place of nothing alike has none.
func TestRewritesArePairedByWhatTheyShare(t *testing.T) {
	head := map[string]string{"a_test.go": "package a\n\nvar t = map[string]string{\n\t`(f \"x\" 0)`: \"start is a number\",\n\t`(f \"x\" 1 0)`: \"count must be at least 1\",\n\t`(g \"y\")`: \"unrelated phrase here\",\n}\n"}
	now := map[string]string{"a_test.go": "package a\n\nvar t = map[string]string{\n\t`(f \"x\" 1 -1)`: \"count must be at least 1\",\n\t`(f \"x\" -1)`: \"start is a number\",\n}\n"}
	rows := removedExpectations(fakeChange(head, now))
	got := map[string]string{}
	for _, r := range rows {
		got[r.text] = r.repl
	}
	want := map[string]string{
		"`(f \"x\" 0)`: \"start is a number\",":          "`(f \"x\" -1)`: \"start is a number\",",
		"`(f \"x\" 1 0)`: \"count must be at least 1\",": "`(f \"x\" 1 -1)`: \"count must be at least 1\",",
		"`(g \"y\")`: \"unrelated phrase here\",":        "",
	}
	for text, repl := range want {
		if got[text] != repl {
			t.Errorf("%s is replaced by %q, want %q", text, got[text], repl)
		}
	}
}

// 5040f9fe: the request has the error name its Content-Type, which adds to
// the phrase a test pins. The attempts that appended to it passed the
// maintainers' test; the one that cut it to "ended without a terminal
// response" and OpenCode's, which put `(content type "text/event-stream")`
// inside it, failed it. The quote a model would most likely give, the
// sentence of the request about that error, supports the append only.
func TestAnAppendedPhraseIsSupportedAndACutOrASplitIsNot(t *testing.T) {
	request := fixture(t, "5040f9fe.request.txt")
	for _, c := range []struct {
		patch string
		want  supportStatus
	}{
		{"5040f9fe.appended.patch", supported},
		{"5040f9fe.shortened.patch", unsupported},
		{"5040f9fe.split.patch", unsupported},
	} {
		r := rowNamed(t, rowsOf(t, c.patch), "want := ")
		quotes := naturalQuote(r, request)
		if len(quotes) == 0 || !strings.Contains(quotes[0], "a stream that ends without a terminal event names its content type in the error") {
			t.Fatalf("%s: the natural quote is %q", c.patch, quotes)
		}
		if got := statusOf(r, request, quotes); got != c.want {
			t.Errorf("%s: %s, want %s (cut %q, put %q)", c.patch, got, c.want, judgeRow(r, request, quotes).cut, judgeRow(r, request, quotes).put)
		}
	}
	// What the shortened row cuts, and the split puts, is said to the model.
	j := judgeRow(rowNamed(t, rowsOf(t, "5040f9fe.shortened.patch"), "want := "), request, nil)
	if !slices.Equal(j.cut, []string{"responses stream"}) {
		t.Errorf("cut %q, want responses stream", j.cut)
	}
	j = judgeRow(rowNamed(t, rowsOf(t, "5040f9fe.split.patch"), "want := "), request, nil)
	if !slices.Equal(j.put, []string{"content type text event stream"}) {
		t.Errorf("put %q, want content type text event stream", j.put)
	}
	// A quote that holds the words cut supports the cut: the request says a
	// "Responses reply is read as a stream", and a model may name it.
	r := rowNamed(t, rowsOf(t, "5040f9fe.shortened.patch"), "want := ")
	if got := statusOf(r, request, requestQuotes(request, "A Responses reply is read as a stream unless it opens as a JSON object")); got != supported {
		t.Errorf("a quote with every word cut: %s", got)
	}
}

// 107251d0: the request has the system prompt go in instructions. A map
// rewrapped with "instructions" in it is a change the request names, and the
// row is supported by what it adds.
func TestARewrappedMapTheRequestAddsToIsSupported(t *testing.T) {
	request := fixture(t, "107251d0.request.txt")
	for _, r := range rowsOf(t, "107251d0.rewrapped.patch") {
		quotes := naturalQuote(r, request)
		if got := statusOf(r, request, quotes); got != supported {
			t.Errorf("%s: %s, quotes %q", r.text, got, quotes)
		}
	}
}

// A row with no word to judge it by is unchecked, whatever is quoted, and a
// row is unsupported when nothing is quoted.
func TestARowWithNoTokenIsUnchecked(t *testing.T) {
	request := fixture(t, "94f5cac4.request.txt")
	quotes := requestQuotes(request, "`glob is every file` is the rule")
	if got := statusOf(removedLine{path: "a_test.go", text: `{"", false, nil},`}, request, quotes); got != unchecked {
		t.Errorf("a row of no tokens: %s", got)
	}
	if got := statusOf(removedLine{path: "a_test.go", text: `"count must be at least 1",`}, request, nil); got != unsupported {
		t.Errorf("nothing quoted: %s", got)
	}
}

// edit-support in a session: the rows as change-removals gives them, the
// request and the reply, a verdict for each in order.
func TestEditSupportPrimitive(t *testing.T) {
	in := scheme.New()
	if err := in.Register(editSupportPrim()); err != nil {
		t.Fatal(err)
	}
	str := func(s string) string { return scheme.Write(scheme.Str(s)) }
	row := func(line int, text string) string {
		return fmt.Sprintf(`(list (cons "path" "a_test.go") (cons "line" %d) (cons "text" %s) (cons "new" ""))`, line, str(text))
	}
	rows := `(list ` + row(59, "`(fs-read \"big.txt\" 0)`:    \"start is a 1-based line number\",") + ` ` + row(60, "`(fs-read \"big.txt\" 1 0)`:  \"count must be at least 1\",") + `)`
	request := str(fixture(t, "94f5cac4.request.txt"))
	show := `(lambda (r) (list (assoc-ref r "line" 0) (assoc-ref r "status" "") (assoc-ref r "quote" "") (assoc-ref r "quoted" #f) (length (assoc-ref r "unmentioned" '()))))`
	v, err := in.EvalString(t.Context(), `(map `+show+` (edit-support `+rows+` `+request+` `+str("`fs-read's start 0 is line 1` covers the first. The second keeps the empty value.")+`))`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.String(), `((59 "supported" "fs read s start 0 is line 1" #t 2) (60 "unsupported" "" #t 2))`; got != want {
		t.Errorf("edit-support = %s, want %s", got, want)
	}
	v, err = in.EvalString(t.Context(), `(map `+show+` (edit-support `+rows+` `+request+` "Restoring both lines."))`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.String(), `((59 "unsupported" "" #f 2) (60 "unsupported" "" #f 2))`; got != want {
		t.Errorf("a reply that quotes nothing: %s, want %s", got, want)
	}
}
