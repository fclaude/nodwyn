package prim

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// edit-support reads the answer to the question asked as an existing test is
// edited ("which sentence of the request requires the new expectation?",
// ADR 32, amended 2026-10-17). On commit-bench 94f5cac4 (dsl22) the question
// fired three times and the model went on: it answered with the request's
// rule for a skipped argument, "gets its type's empty value", and kept the
// rewritten row `(fs-read "big.txt" 1 0)` "count must be at least 1", which
// the request never mentions. An answer is now a line for each row: the row,
// then a sentence copied from the request that names the input or case the
// row tests. This reads it, and judges nothing but the text: whether a quote
// is in the request, and whether it is about the row.
//
// A quote is a run of at least supportQuoteWords words of the answer that
// stands in the request in the same order, compared with the case, the
// punctuation and the white space set aside. It is about a row when it shares
// a distinctive token with the row's string literals, its input
// (`(fs-read "big.txt" 1 0)`, `{"path":"p","count":4}`) and the message it
// expects ("count must be at least 1"), and with the line that replaces the
// row, if the change put one in its place. A token is a word of 3 letters or
// more, or a number from 2 up (0 and 1 are in every sentence about an index
// or a count), stemmed lightly (lines and line, ends and ended are one token),
// that is no stop word and no part of the name of a function the row calls
// (fs-read: fs, read), since a sentence that names the function says nothing
// of which case of it the row tests. A row with no token (`{"", false, nil},`)
// cannot be judged: it is unchecked, and left to the finish review's other
// point.
//
// A rewrite that cuts words out of the phrase a row expects, or puts words
// in the middle of it, needs more: the quote must hold most of the words cut,
// or all of the words put (phraseEdits). That is the difference on 5040f9fe
// between the attempts that appended " (Content-Type: text/event-stream)" to
// the pinned phrase, which the request asks for and the maintainers' test
// accepts, and the ones that cut it to "ended without a terminal response" or
// put `(content type "text/event-stream")` inside it, which fail that test.
const supportQuoteWords = 3

// supportStatus is what the text can say about a row.
type supportStatus string

const (
	supported   supportStatus = "supported"
	unsupported supportStatus = "unsupported"
	unchecked   supportStatus = "unchecked"
)

// rowSupport is the verdict on a row: the quote that supports it, the
// literals of the row the request does not contain (what a firmer message
// can say the request does not mention), and the words a rewrite cuts from
// its phrase and puts inside it (phraseEdits), which a quote must hold.
type rowSupport struct {
	status      supportStatus
	quote       string
	unmentioned []string
	cut, put    []string
}

var supportStop = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`the and for are was were has have had not but you your all any can may will would should could must
		this that these those with from into onto than then else when where which what who whom whose how why there here their them they its
		out off over under about after before above below between through during again once only own same such very just also more most
		other some each both few too per via one two got get gets want wants wanted expect expects expected actual error errors err nil true false
		fail fails failed test tests case cases ok yes none`) {
		m[w] = true
	}
	return m
}()

// codeNoise is the words of Go and Python code, and of their test helpers,
// that tell nothing of what a line tests; they are set aside when a line is
// read by its identifiers.
var codeNoise = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`func return range len string strings bytes contains hasprefix hassuffix equal equalfold assert require
		fatal fatalf errorf error got raw req resp self def elif except raise import int bool byte any map var const type struct
		new make append testing eval`) {
		m[w] = true
	}
	return m
}()

// supportWords is the words of text in small letters, a word being a run of
// letters and digits.
func supportWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// stemToken sets aside the endings that make two forms of a word two tokens:
// plurals and the -ed and -ing forms.
func stemToken(w string) string {
	switch {
	case len(w) > 5 && strings.HasSuffix(w, "ing"):
		return w[:len(w)-3]
	case len(w) > 4 && strings.HasSuffix(w, "ed"):
		return w[:len(w)-2]
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return w[:len(w)-1]
	}
	return w
}

// callNameRE is a name in the position of a call: (fs-read ... or fsRead(.
var callNameRE = regexp.MustCompile(`\(\s*([A-Za-z_][\w.\-:]*)|\b([A-Za-z_][\w.\-]*)\(`)

// supportTokens is the distinctive tokens of some texts: no stop word, no
// word under 3 letters, no 0 or 1, and no part of the name of a function
// called in calls.
func supportTokens(texts []string, calls []string) map[string]bool {
	skip := map[string]bool{}
	for _, c := range calls {
		for _, m := range callNameRE.FindAllStringSubmatch(c, -1) {
			for _, part := range supportWords(m[1] + m[2]) {
				skip[stemToken(part)] = true
			}
		}
	}
	out := map[string]bool{}
	for _, text := range texts {
		for _, w := range supportWords(text) {
			if n, err := strconv.Atoi(w); err == nil {
				if n < 2 {
					continue
				}
			} else if len(w) < 3 || supportStop[w] {
				continue
			}
			if t := stemToken(w); !skip[t] && !supportStop[t] {
				out[t] = true
			}
		}
	}
	return out
}

// identifierWords is the words of the identifiers in a line's code, outside
// its string literals and comments, camel case split (RoleSystem: role,
// system), the words of code set aside.
func identifierWords(rel, line string) []string {
	code := codePart(rel, line)
	var plain strings.Builder
	var quote byte
	for i := 0; i < len(code); i++ {
		ch := code[i]
		switch {
		case quote != 0:
			if ch == '\\' && quote != '`' {
				i++
			} else if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '`' || ch == '\'':
			quote = ch
			plain.WriteByte(' ')
		default:
			plain.WriteByte(ch)
		}
	}
	var out []string
	for _, id := range strings.FieldsFunc(plain.String(), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		start := 0
		runes := []rune(id)
		for i := 1; i <= len(runes); i++ {
			if i == len(runes) || unicode.IsUpper(runes[i]) && (unicode.IsLower(runes[i-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1])) {
				if w := strings.ToLower(string(runes[start:i])); !codeNoise[w] {
					out = append(out, w)
				}
				start = i
			}
		}
	}
	return out
}

// rowTokens is the distinctive tokens of an expectation line and of the
// line that replaces it: those of its string literals, its input and the
// message it expects, and, when those give none, those of its identifiers
// too (`{Role: RoleSystem, Content: ""},`).
func rowTokens(r removedLine) map[string]bool {
	lits := nonEmpty(stringLiterals(r.path, r.text))
	texts := lits
	if r.repl != "" {
		texts = append(texts, nonEmpty(stringLiterals(r.path, r.repl))...)
	}
	out := supportTokens(texts, texts)
	if len(out) == 0 {
		words := identifierWords(r.path, r.text)
		if r.repl != "" {
			words = append(words, identifierWords(r.path, r.repl)...)
		}
		for t := range supportTokens(words, nil) {
			out[t] = true
		}
	}
	return out
}

// phraseEdits is how a rewrite changes the phrase a row pins, the last string
// literal on it (the message a case expects, the want of a check): the words
// it takes out (cut) and the words it puts in the middle of the phrase or in
// place of some of it (put), as the phrase and the replacement's last literal
// differ after their common first and last words. A phrase the replacement
// only adds to at its end or its start is no edit: that is the append the
// question asks for. On 5040f9fe the request says the error names the
// Content-Type, and the attempts that appended " (Content-Type:
// text/event-stream)" to the pinned phrase passed the maintainers' test, while
// the one that cut it to "ended without a terminal response" (cut:
// "responses stream") and OpenCode's, which put `(content type
// "text/event-stream")` inside it (put: "content type text event stream"),
// failed it. A stretch of one word is a name or a number, which the tokens
// judge.
func phraseEdits(r removedLine) (cut, put []string) {
	if r.repl == "" {
		return nil, nil
	}
	olds, news := nonEmpty(stringLiterals(r.path, r.text)), nonEmpty(stringLiterals(r.path, r.repl))
	if len(olds) == 0 || len(news) == 0 {
		return nil, nil
	}
	ow, nw := supportWords(olds[len(olds)-1]), supportWords(news[len(news)-1])
	p := 0
	for p < len(ow) && p < len(nw) && ow[p] == nw[p] {
		p++
	}
	s := 0
	for s < len(ow)-p && s < len(nw)-p && ow[len(ow)-1-s] == nw[len(nw)-1-s] {
		s++
	}
	switch {
	case len(ow) < 3, 2*(p+s) < len(ow), p == len(ow), s == len(ow):
		return nil, nil // not the same phrase, or kept whole at its start or its end
	}
	if gone := ow[p : len(ow)-s]; len(gone) > 1 && len(nw) == p+s {
		cut = append(cut, strings.Join(gone, " "))
	} else if added := nw[p : len(nw)-s]; len(added) > 1 {
		put = append(put, strings.Join(added, " "))
	}
	return cut, put
}

// mostly is whether the quote holds more than half of the distinctive tokens
// of a run of words: the stretch a rewrite cuts is named by a sentence that
// says most of what goes, not one word of it ("A stream that ends without a
// terminal event" names "stream" and not "responses").
func mostly(quote, run string) bool {
	want := supportTokens([]string{run}, nil)
	if len(want) == 0 {
		return true
	}
	have := supportTokens([]string{quote}, nil)
	n := 0
	for t := range want {
		if have[t] {
			n++
		}
	}
	return 2*n > len(want)
}

// indexWords is where the words sub stand in words, in a row, or -1.
func indexWords(words, sub []string) int {
	for i := 0; i+len(sub) <= len(words); i++ {
		if slices.Equal(words[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// requestQuotes is the quotes the answer makes of the request: the runs of at
// least supportQuoteWords words of the answer that stand together, in order,
// in the request, longest first from the left, each once.
func requestQuotes(request, answer string) []string {
	req, ans := supportWords(request), supportWords(answer)
	at := map[string][]int{}
	for i, w := range req {
		at[w] = append(at[w], i)
	}
	var out []string
	for i := 0; i < len(ans); {
		best := 0
		for _, j := range at[ans[i]] {
			n := 0
			for i+n < len(ans) && j+n < len(req) && ans[i+n] == req[j+n] {
				n++
			}
			best = max(best, n)
		}
		if best >= supportQuoteWords {
			q := strings.Join(ans[i:i+best], " ")
			if !slices.Contains(out, q) {
				out = append(out, q)
			}
			i += best
		} else {
			i++
		}
	}
	return out
}

// judgeRow sets a row against the quotes of an answer.
func judgeRow(r removedLine, request string, quotes []string) rowSupport {
	tokens := rowTokens(r)
	cut, put := phraseEdits(r)
	res := rowSupport{status: unsupported, unmentioned: unmentioned(r, request), cut: cut, put: put}
	if len(tokens) == 0 {
		res.status = unchecked
		return res
	}
	best := 0
	for _, q := range quotes {
		shared := 0
		for t := range supportTokens([]string{q}, nil) {
			if tokens[t] {
				shared++
			}
		}
		if shared == 0 || shared <= best {
			continue
		}
		held := true
		for _, run := range cut {
			held = held && mostly(q, run)
		}
		words := strings.Fields(q)
		for _, run := range put {
			held = held && indexWords(words, strings.Fields(run)) >= 0
		}
		if held {
			best, res.quote, res.status = shared, q, supported
		}
	}
	return res
}

// unmentioned is the string literals of a row that the request does not
// contain, as the request is read for a quote: the input and the expectation
// that it never gives.
func unmentioned(r removedLine, request string) []string {
	have := " " + strings.Join(supportWords(request), " ") + " "
	var out []string
	for _, l := range nonEmpty(stringLiterals(r.path, r.text)) {
		w := strings.Join(supportWords(l), " ")
		if w != "" && !strings.Contains(have, " "+w+" ") && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

func editSupportPrim() *scheme.Prim {
	return &scheme.Prim{
		Name: "edit-support",
		Meta: scheme.Meta{
			Doc: "Reads an answer to \"which sentence of the request requires the new expectation?\" for lines of existing tests the turn changed (the expectations of change-removals, each with its text and, when the change put another line in its place, new): a list with an alist for each line, in order, of path, line, text, status, quote, quoted, unmentioned, cut and put. The answer quotes the request when a run of " + fmt.Sprint(supportQuoteWords) + " words or more of it stands in the request in the same order, case, punctuation and white space aside; quoted says whether it does so at all. status is \"supported\" when a quote shares a distinctive token with the line and its replacement (quote is the best one), \"unsupported\" when none does, \"unchecked\" when the line has no token to judge by. A token is a word of 3 letters or more or a number from 2 up, lightly stemmed, that is no stop word and no part of the name of a function the line calls (words of identifiers count when its string literals give none). When the replacement cuts words out of the phrase the line expects, or puts words in the middle of it, a supporting quote also holds most of the words cut or all of the words put (cut and put list them). unmentioned lists the string literals of the line the request does not contain. Hidden; judges text only. The evidence loop reads the reply that follows the question (ADR 32, amended 2026-10-17).",
			Params: []scheme.Param{
				{Name: "lines", Type: "list", Doc: "alists of path, line and text"},
				{Name: "request", Type: "string", Doc: "the request"},
				{Name: "answer", Type: "string", Doc: "the model's reply"},
			},
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			lines, err := c.List(0)
			if err != nil {
				return scheme.Value{}, err
			}
			request, err := c.String(1)
			if err != nil {
				return scheme.Value{}, err
			}
			answer, err := c.String(2)
			if err != nil {
				return scheme.Value{}, err
			}
			quotes := requestQuotes(request, answer)
			out := make([]scheme.Value, 0, len(lines))
			for _, l := range lines {
				r := removedLine{}
				cells, _ := l.Slice()
				for _, cell := range cells {
					if cell.Kind != scheme.KPair || cell.P.Car.Kind != scheme.KString {
						continue
					}
					switch v := cell.P.Cdr; cell.P.Car.S {
					case "path":
						r.path = v.S
					case "text":
						r.text = v.S
					case "new":
						r.repl = v.S
					case "line":
						r.line = int(v.I)
					}
				}
				j := judgeRow(r, request, quotes)
				miss := make([]scheme.Value, len(j.unmentioned))
				for i, m := range j.unmentioned {
					miss[i] = scheme.Str(m)
				}
				cut, put := make([]scheme.Value, len(j.cut)), make([]scheme.Value, len(j.put))
				for i, c := range j.cut {
					cut[i] = scheme.Str(c)
				}
				for i, p := range j.put {
					put[i] = scheme.Str(p)
				}
				out = append(out, scheme.List(
					scheme.Cons(scheme.Str("path"), scheme.Str(r.path)),
					scheme.Cons(scheme.Str("line"), scheme.Int(int64(r.line))),
					scheme.Cons(scheme.Str("text"), scheme.Str(r.text)),
					scheme.Cons(scheme.Str("status"), scheme.Str(string(j.status))),
					scheme.Cons(scheme.Str("quote"), scheme.Str(j.quote)),
					scheme.Cons(scheme.Str("quoted"), scheme.Bool(len(quotes) > 0)),
					scheme.Cons(scheme.Str("unmentioned"), scheme.List(miss...)),
					scheme.Cons(scheme.Str("cut"), scheme.List(cut...)),
					scheme.Cons(scheme.Str("put"), scheme.List(put...))))
			}
			return scheme.List(out...), nil
		},
	}
}
