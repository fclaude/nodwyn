package prim

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// change-removals says what an uncommitted change takes away that its own
// check cannot show: the definitions it removes and the expectations of
// existing tests it removes or rewrites (ADR 32, amended 2026-10-15). On
// commit-bench a hidden test called evalOr, an unexported helper the task
// never named, and dsl16 and Pi fixed the behaviour by deleting it as dead
// code, so their patches did not build against the hidden test (67a1f950);
// Pi turned the visible assertion that count 0 is an error into one for -1
// (94f5cac4) and deleted the negative case its new pattern broke
// (ef04de9c), and its own runs then passed on the tests it had rewritten.
// The evidence loop reads this before an unattended turn ends on a passing
// check; what it says is advisory, since 6 of 73 original commits delete a
// function and 39 edit existing test lines.

const (
	removalsMaxDefs  = 6
	removalsMaxLines = 8
)

// changeFiles is an uncommitted change as the finish notes read it: the
// paths git lists as changed, each file as HEAD has it and as it is now, and
// the files a directory holds now. The notes are pure functions of it, so
// they read a patch the same way offline.
type changeFiles struct {
	paths []string
	head  func(rel string) ([]byte, bool)
	now   func(rel string) ([]byte, bool)
	dir   func(dir string) []string
}

// workChange is the change in the working tree against HEAD. Files the
// denylist or a protected path covers, links and files over withoutMaxFile
// are not read.
func workChange(ctx context.Context, o Options) (changeFiles, error) {
	k := &Keeper{o: o}
	_, paths, err := k.look(ctx)
	if err != nil {
		return changeFiles{}, err
	}
	tracked, err := k.headFiles(ctx)
	if err != nil {
		return changeFiles{}, err
	}
	deny := denylist(o)
	read := func(rel string) ([]byte, bool) {
		if !keepable(deny, rel) {
			return nil, false
		}
		abs := filepath.Join(o.Root, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		if err != nil || !info.Mode().IsRegular() || info.Size() > withoutMaxFile {
			return nil, false
		}
		data, err := os.ReadFile(abs)
		return data, err == nil
	}
	return changeFiles{
		paths: paths,
		head: func(rel string) ([]byte, bool) {
			h, ok := tracked[rel]
			if !ok || !keepable(deny, rel) {
				return nil, false
			}
			data, err := k.headBlob(ctx, h.oid)
			return data, err == nil && len(data) <= withoutMaxFile
		},
		now: read,
		dir: func(dir string) []string {
			entries, err := os.ReadDir(filepath.Join(o.Root, filepath.FromSlash(dir)))
			if err != nil {
				return nil
			}
			var out []string
			for _, e := range entries {
				if e.Type().IsRegular() {
					out = append(out, path.Join(dir, e.Name()))
				}
			}
			return out
		},
	}, nil
}

// removedDef is a top-level definition HEAD has in a changed file that the
// change leaves nowhere in its package.
type removedDef struct {
	path, name, kind string
	line             int // its line at HEAD
}

// removedLine is a line of an existing test that held an expectation and
// that the change removes or rewrites.
type removedLine struct {
	path string
	line int // its line at HEAD
	text string
}

// definedNames is the top-level names a source file defines, with their
// kind and line, by language: a Go file's functions, methods (as
// Type.Method), types, variables and constants; a Python file's functions
// and classes; a Scheme file's defines. ok is false for a file it cannot
// read, a Go file that does not parse among them.
func definedNames(rel string, src []byte) (names map[string]removedDef, ok bool) {
	names = map[string]removedDef{}
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go":
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, false
		}
		add := func(name, kind string, pos token.Pos) {
			if name != "_" && name != "init" {
				names[name] = removedDef{path: rel, name: name, kind: kind, line: fset.Position(pos).Line}
			}
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil && len(d.Recv.List) == 1 {
					add(receiverName(d.Recv.List[0].Type)+"."+d.Name.Name, "method", d.Pos())
				} else {
					add(d.Name.Name, "func", d.Pos())
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						add(s.Name.Name, "type", s.Pos())
					case *ast.ValueSpec:
						kind := "var"
						if d.Tok == token.CONST {
							kind = "const"
						}
						for _, n := range s.Names {
							add(n.Name, kind, n.Pos())
						}
					}
				}
			}
		}
		return names, true
	case ".py":
		return regexNames(rel, src, pyDefRE, "def"), true
	case ".scm":
		return regexNames(rel, src, scmDefRE, "define"), true
	}
	return nil, false
}

var (
	pyDefRE  = regexp.MustCompile(`(?m)^(?:async\s+def|def|class)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	scmDefRE = regexp.MustCompile(`(?m)^\(define\s+\(?([^\s()]+)`)
)

func regexNames(rel string, src []byte, re *regexp.Regexp, kind string) map[string]removedDef {
	names := map[string]removedDef{}
	for _, m := range re.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		if _, seen := names[name]; !seen {
			names[name] = removedDef{path: rel, name: name, kind: kind, line: 1 + strings.Count(string(src[:m[0]]), "\n")}
		}
	}
	return names
}

// receiverName is a method's receiver type without its pointer and type
// parameters: Keeper for (k *Keeper) and for (s Set[T]).
func receiverName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return "?"
		}
	}
}

// sourceFile is whether rel is code whose definitions a change may remove:
// Go, Python or Scheme, and not a test.
func sourceFile(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go", ".py", ".scm":
		return !testSide(rel)
	}
	return false
}

// removedDefinitions is the top-level definitions HEAD has in the change's
// files that no file of the same language in the same directory defines now
// (a package, or a module's neighbours), the names the request gives left
// out. A name in another case counts as removed: a test calls it by name.
func removedDefinitions(c changeFiles, request string) []removedDef {
	var out []removedDef
	dirs := map[string]map[string]bool{} // dir+ext: the names defined there now
	for _, rel := range c.paths {
		if !sourceFile(rel) {
			continue
		}
		old, ok := c.head(rel)
		if !ok {
			continue
		}
		before, ok := definedNames(rel, old)
		if !ok {
			continue
		}
		var gone []removedDef
		if cur, ok := c.now(rel); ok {
			after, ok := definedNames(rel, cur)
			if !ok {
				continue // it does not parse now: what it defines is unknown
			}
			for name, d := range before {
				if _, kept := after[name]; !kept {
					gone = append(gone, d)
				}
			}
		} else {
			for _, d := range before {
				gone = append(gone, d)
			}
		}
		if len(gone) == 0 {
			continue
		}
		ext := strings.ToLower(filepath.Ext(rel))
		key := path.Dir(rel) + "\x00" + ext
		there, seen := dirs[key]
		if !seen {
			there = map[string]bool{}
			for _, p := range c.dir(path.Dir(rel)) {
				if p == rel || strings.ToLower(filepath.Ext(p)) != ext || !sourceFile(p) {
					continue
				}
				if src, ok := c.now(p); ok {
					names, _ := definedNames(p, src)
					for n := range names {
						there[n] = true
					}
				}
			}
			dirs[key] = there
		}
		for _, d := range gone {
			if there[d.name] || namedBy(request, d.name) {
				continue
			}
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}
		return out[i].line < out[j].line
	})
	if len(out) > removalsMaxDefs {
		out = out[:removalsMaxDefs]
	}
	return out
}

// namedBy is whether request names a definition: whole (hasName), or in
// words that spell it once case and separators are set aside, one word or
// two in a row ("the guard's workspace-kind checker is gone" for
// WorkspaceKind, "the first known finding" for firstKnown). A method is
// named whole (Type.Method) or by its type, unless the type is a plain word
// (server): its own name (Check, Name) is too common a word, and so is such
// a type's.
func namedBy(request, name string) bool {
	if typ, _, method := strings.Cut(name, "."); method {
		return hasName(request, name) || typ != strings.ToLower(typ) && namedBy(request, typ)
	}
	if hasName(request, name) {
		return true
	}
	want := squashName(name)
	if len(want) < 4 {
		return false
	}
	words := requestWordRE.FindAllString(request, -1)
	for i, w := range words {
		if squashName(w) == want || i+1 < len(words) && squashName(w+words[i+1]) == want {
			return true
		}
	}
	return false
}

var requestWordRE = regexp.MustCompile(`[A-Za-z0-9_-]+`)

// squashName is a name in small letters and digits only.
func squashName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// expectationTest is whether rel is a test file whose removed expectations
// are read: Go's x_test.go, Python's test_x.py and x_test.py.
func expectationTest(rel string) bool {
	base := filepath.Base(rel)
	return strings.HasSuffix(base, "_test.go") ||
		strings.HasSuffix(base, ".py") && (strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py"))
}

var (
	goAssertRE   = regexp.MustCompile(`\b(assert|require)\.[A-Z]\w*\(`)
	pyAssertRE   = regexp.MustCompile(`^assert\b|\bself\.assert[A-Z]\w*\(|\bpytest\.raises\b|\.assert_\w+\(`)
	literalLeads = regexp.MustCompile("[\"'`]|\\b\\d|\\b(true|false|nil|None|True|False)\\b")
)

// expectationLine is whether a test's line states what is expected: a case
// of a table (a line ending in a comma that holds a literal: `{"1 0",
// "count must be at least 1"},`), or an assertion that carries its
// expectation (testify's assert.Equal, Python's assert). Both losses on the
// benchmark were cases of a table. A t.Errorf's message says nothing of what
// is expected, and the if before it is rewritten as often as a test is
// refactored: counted, they flagged 84 of the 306 patches replayed, against
// the 62 that cases and assertions flag.
func expectationLine(rel, line string) bool {
	code := strings.TrimSpace(codePart(rel, line))
	switch {
	case code == "":
		return false
	case strings.HasSuffix(rel, ".py") && pyAssertRE.MatchString(code), strings.HasSuffix(rel, ".go") && goAssertRE.MatchString(code):
		return true
	}
	return strings.HasSuffix(code, ",") && literalLeads.MatchString(code)
}

// codePart is a line without its trailing comment (// in Go, # in Python),
// a marker inside quotes left alone.
func codePart(rel, line string) string {
	marker := "//"
	if strings.HasSuffix(rel, ".py") {
		marker = "#"
	}
	var quote byte
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case quote != 0:
			if ch == '\\' && quote != '`' {
				i++
			} else if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'' || ch == '`':
			quote = ch
		case strings.HasPrefix(line[i:], marker):
			return line[:i]
		}
	}
	return line
}

// normalLine is a line as two versions of it compare: its code, with every
// run of white space one space, so gofmt's realigned columns are the same
// line.
func normalLine(rel, line string) string {
	return strings.Join(strings.Fields(codePart(rel, line)), " ")
}

// removedExpectations is the expectation lines (expectationLine) of the
// tests HEAD has that the change removes and adds nowhere in its test files,
// as whitespace and trailing comments aside they are: a case moved to
// another test file, or realigned, is not removed.
func removedExpectations(c changeFiles) []removedLine {
	added := map[string]bool{}
	type change struct {
		rel     string
		removed []removedLine
	}
	var changes []change
	for _, rel := range c.paths {
		if !expectationTest(rel) {
			continue
		}
		cur, hasNow := c.now(rel)
		old, hasHead := c.head(rel)
		var a, b []string
		if hasHead {
			a = splitLines(string(old))
		}
		if hasNow {
			b = splitLines(string(cur))
		}
		if len(a)+len(b) > withoutMaxLines {
			continue
		}
		ch := change{rel: rel}
		for _, op := range myers(a, b) {
			switch op.kind {
			case '+':
				added[normalLine(rel, op.line)] = true
			case '-':
				if expectationLine(rel, op.line) {
					ch.removed = append(ch.removed, removedLine{path: rel, line: op.ai + 1, text: strings.TrimSpace(op.line)})
				}
			}
		}
		changes = append(changes, ch)
	}
	var out []removedLine
	for _, ch := range changes {
		for _, r := range ch.removed {
			if !added[normalLine(ch.rel, r.text)] {
				out = append(out, r)
			}
		}
	}
	if len(out) > removalsMaxLines {
		out = out[:removalsMaxLines]
	}
	return out
}

func changeRemovalsPrim(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "change-removals",
		Meta: scheme.Meta{
			Doc:    "What the uncommitted change takes away that its own check cannot show: an alist of definitions, the top-level definitions (Go functions, methods as Type.Method, types, variables and constants; Python functions and classes; Scheme defines) HEAD has in a changed non-test file that no file of the same language in its directory defines now, names request gives left out, at most " + fmt.Sprint(removalsMaxDefs) + ", each an alist of path, name, kind and line (at HEAD); and expectations, the lines of existing tests (x_test.go, test_x.py, x_test.py) that state an expectation (a case of a table: a line ending in a comma that holds a literal; or an assertion that carries one: testify's assert and require, Python's assert) and that the change removes and adds back nowhere in its test files, white space and trailing comments aside, at most " + fmt.Sprint(removalsMaxLines) + ", each an alist of path, line (at HEAD) and text. Hidden; registered in a git work tree. The evidence loop reads it before an unattended turn ends on a passing check (ADR 32, amended 2026-10-15).",
			Params: []scheme.Param{{Name: "request", Type: "string", Doc: "the request: a name it gives is not counted as removed"}},
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			request, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			ch, err := workChange(c.Ctx, o)
			if err != nil {
				return scheme.Value{}, fmt.Errorf("change-removals: %w", err)
			}
			var defs, lines []scheme.Value
			for _, d := range removedDefinitions(ch, request) {
				defs = append(defs, scheme.List(
					scheme.Cons(scheme.Str("path"), scheme.Str(d.path)),
					scheme.Cons(scheme.Str("name"), scheme.Str(d.name)),
					scheme.Cons(scheme.Str("kind"), scheme.Str(d.kind)),
					scheme.Cons(scheme.Str("line"), scheme.Int(int64(d.line)))))
			}
			for _, l := range removedExpectations(ch) {
				lines = append(lines, scheme.List(
					scheme.Cons(scheme.Str("path"), scheme.Str(l.path)),
					scheme.Cons(scheme.Str("line"), scheme.Int(int64(l.line))),
					scheme.Cons(scheme.Str("text"), scheme.Str(l.text))))
			}
			return scheme.List(
				scheme.Cons(scheme.Str("definitions"), scheme.List(defs...)),
				scheme.Cons(scheme.Str("expectations"), scheme.List(lines...))), nil
		},
	}
}
