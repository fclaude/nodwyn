package prim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/guard"
)

// outline gives the model the shape of a file without its text: each class,
// function, method and module-level variable with the lines it spans, its
// signature and the first line of its documentation, nested under what holds
// it. On the benchmark's transcripts (Muse, 263 sessions with an edit) 9.7 of
// about 21 calls came before the first edit, 7.4 of them reading and
// searching; 40% of the characters read were of files the session never
// edited, and every non-shell run opened with a whole-file read (136,192
// characters of matplotlib's figure.py), then grepped and read ranges of the
// same file. The model also guessed ranges wrong (matplotlib c12) and
// unittest ids wrong, because a ranged read shows a method but not its class
// (ADR 32, amended 2026-10-17).
//
// It parses nothing itself that a tool does better. Go is read with go/parser,
// in process. Python is read by the interpreter's own ast module (a fixed
// script that parses the text and never runs it; python3 -I; no code of the
// repository is imported). Any other language is read by universal-ctags
// when the machine has it, and by a few line patterns when it has not.

const (
	outlineMaxSymbols = 250
	outlineMaxBytes   = 12000
	outlineTimeout    = 15 * time.Second
	outlineMaxFile    = 4 << 20
)

// symbol is one definition of a file.
type symbol struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Sig   string `json:"sig"`
	Doc   string `json:"doc"`
	Line  int    `json:"line"`
	End   int    `json:"end"`
	Depth int    `json:"depth"`
	Scope string `json:"scope"` // the qualified name of what holds it
}

// qualified is the name a symbol is read by: Class.method.
func (s symbol) qualified() string {
	if s.Scope == "" {
		return s.Name
	}
	return s.Scope + "." + s.Name
}

func outlinePrim(o Options, pin guard.RepoPin) *scheme.Prim {
	return &scheme.Prim{
		Name: "outline",
		Meta: scheme.Meta{
			Doc: "The shape of a source file without its text: its classes, functions, methods and module-level variables, nested under what holds them, each with the lines it spans, its signature and the first line of its documentation. Python and Go are parsed exactly; other languages by universal-ctags when installed, else by line patterns. Read one of them with fs-read and symbol (for example Class.method), which gives that definition's lines under its header.",
			Params: []scheme.Param{
				{Name: "path", Type: "string", Doc: "source file relative to the repository root", Role: "path"},
			},
			Host:        true,
			TaintSource: scheme.TagFile,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			abs, err := resolve(pin, c, 0)
			if err != nil {
				return scheme.Value{}, err
			}
			text, err := outlineText(c.Ctx, o, abs, c.Args[0].S)
			if err != nil {
				return scheme.Value{}, err
			}
			return scheme.Str(text), nil
		},
	}
}

// outlineSymbols reads the definitions of the file at abs.
func outlineSymbols(ctx context.Context, o Options, abs string) (syms []symbol, how string, lines int, err error) {
	st, err := os.Stat(abs)
	if err != nil {
		return nil, "", 0, err
	}
	if st.IsDir() {
		return nil, "", 0, errors.New("a directory has no outline: outline takes a file")
	}
	if st.Size() > outlineMaxFile {
		return nil, "", 0, fmt.Errorf("%d bytes is too large to outline", st.Size())
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		return nil, "", 0, err
	}
	lines = strings.Count(string(src), "\n")
	if len(src) > 0 && src[len(src)-1] != '\n' {
		lines++
	}
	switch strings.ToLower(filepath.Ext(abs)) {
	case ".go":
		if syms, err = goSymbols(src); err == nil {
			return syms, "go/parser", lines, nil
		}
	case ".py", ".pyi":
		if syms, err = pythonSymbols(ctx, o, abs); err == nil {
			return syms, "python ast", lines, nil
		}
	}
	if syms, err = ctagsSymbols(ctx, o, abs); err == nil && len(syms) > 0 {
		return syms, "universal-ctags", lines, nil
	}
	return patternSymbols(src), "line patterns", lines, nil
}

func outlineText(ctx context.Context, o Options, abs, shown string) (string, error) {
	syms, how, lines, err := outlineSymbols(ctx, o, abs)
	if err != nil {
		return "", err
	}
	return renderOutline(shown, syms, how, lines), nil
}

func renderOutline(path string, syms []symbol, how string, lines int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d lines, %d definitions (%s)\n", path, lines, len(syms), how)
	if len(syms) == 0 {
		b.WriteString("no definitions found: read the file with fs-read\n")
		return b.String()
	}
	for i, s := range syms {
		if i >= outlineMaxSymbols || b.Len() > outlineMaxBytes {
			fmt.Fprintf(&b, "… %d more definitions: read a region with fs-read (start, count), or a definition with fs-read and symbol\n", len(syms)-i)
			break
		}
		span := fmt.Sprintf("%d", s.Line)
		if s.End > s.Line {
			span = fmt.Sprintf("%d-%d", s.Line, s.End)
		}
		head := strings.TrimSpace(s.Kind + " " + s.Name + s.Sig)
		if s.Doc != "" {
			head += "  — " + s.Doc
		}
		fmt.Fprintf(&b, "%s%-9s %s\n", strings.Repeat("  ", s.Depth), span, head)
	}
	return b.String()
}

// findSymbol is the definition a name asks for: the whole qualified name
// (Class.method), or a bare name that only one definition has.
func findSymbol(syms []symbol, name string) (symbol, error) {
	name = strings.TrimSpace(name)
	var exact, bare []symbol
	for _, s := range syms {
		switch {
		case s.qualified() == name:
			exact = append(exact, s)
		case s.Name == name || strings.HasSuffix(s.qualified(), "."+name):
			bare = append(bare, s)
		}
	}
	switch {
	case len(exact) >= 1:
		return exact[0], nil
	case len(bare) == 1:
		return bare[0], nil
	case len(bare) > 1:
		var names []string
		for _, s := range bare {
			names = append(names, fmt.Sprintf("%s (line %d)", s.qualified(), s.Line))
		}
		if len(names) > 8 {
			names = append(names[:8], "…")
		}
		return symbol{}, fmt.Errorf("%q names %d definitions: %s; give the qualified name", name, len(bare), strings.Join(names, ", "))
	}
	return symbol{}, fmt.Errorf("no definition named %q: outline lists them", name)
}

// ---- Go ----

func goSymbols(src []byte) ([]symbol, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	line := func(p token.Pos) int { return fset.Position(p).Line }
	first := func(cg *ast.CommentGroup) string {
		if cg == nil {
			return ""
		}
		t := strings.TrimSpace(cg.Text())
		if i := strings.IndexByte(t, '\n'); i >= 0 {
			t = t[:i]
		}
		return clipDoc(t)
	}
	text := func(a, b token.Pos) string {
		return strings.Join(strings.Fields(string(src[fset.Position(a).Offset:fset.Position(b).Offset])), " ")
	}
	var out []symbol
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			s := symbol{Name: d.Name.Name, Kind: "func", Line: line(d.Pos()), End: line(d.End()), Doc: first(d.Doc)}
			end := d.Type.End()
			s.Sig = text(d.Name.End(), end)
			if d.Recv != nil && len(d.Recv.List) > 0 {
				s.Kind = "method"
				recv := text(d.Recv.List[0].Type.Pos(), d.Recv.List[0].Type.End())
				s.Scope = strings.TrimPrefix(recv, "*")
				if i := strings.IndexByte(s.Scope, '['); i >= 0 {
					s.Scope = s.Scope[:i]
				}
			}
			if d.Doc != nil {
				s.Line = line(d.Doc.Pos())
			}
			out = append(out, s)
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					kind := "type"
					switch sp.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					doc := sp.Doc
					if doc == nil {
						doc = d.Doc
					}
					out = append(out, symbol{Name: sp.Name.Name, Kind: kind, Line: line(sp.Pos()), End: line(sp.End()), Doc: first(doc)})
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						if n.Name == "_" {
							continue
						}
						kind := strings.ToLower(d.Tok.String())
						doc := sp.Doc
						if doc == nil {
							doc = d.Doc
						}
						out = append(out, symbol{Name: n.Name, Kind: kind, Line: line(sp.Pos()), End: line(sp.End()), Doc: first(doc)})
					}
				}
			}
		}
	}
	// A method is shown under its type when the file declares it, else flat.
	return nestByScope(out), nil
}

// nestByScope orders symbols so a method follows its type, and gives each its
// depth; a scope the file does not declare leaves the method at depth 0.
func nestByScope(syms []symbol) []symbol {
	declared := map[string]bool{}
	for _, s := range syms {
		if s.Scope == "" {
			declared[s.Name] = true
		}
	}
	byScope := map[string][]symbol{}
	var top []symbol
	for _, s := range syms {
		if s.Scope != "" && declared[s.Scope] {
			byScope[s.Scope] = append(byScope[s.Scope], s)
			continue
		}
		top = append(top, s)
	}
	var out []symbol
	for _, s := range top {
		out = append(out, s)
		for _, m := range byScope[s.Name] {
			m.Depth = 1
			out = append(out, m)
		}
	}
	return out
}

func clipDoc(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 110 {
		s = s[:110] + "…"
	}
	return s
}

// ---- Python ----

// pythonScript lists a Python file's definitions from its syntax tree. It
// never executes the file. end_lineno exists from Python 3.8; before that a
// definition ends where the next sibling begins.
const pythonScript = `
import ast, json, sys
path = sys.argv[1]
src = open(path, encoding="utf-8", errors="replace").read()
tree = ast.parse(src)
total = src.count("\n") + 1
out = []
def sig(n):
    a = n.args
    parts = []
    pos = list(getattr(a, "posonlyargs", [])) + list(a.args)
    nd = len(pos) - len(a.defaults)
    for i, x in enumerate(pos):
        parts.append(x.arg + ("=…" if i >= nd else ""))
    if a.vararg: parts.append("*" + a.vararg.arg)
    elif a.kwonlyargs: parts.append("*")
    for x, d in zip(a.kwonlyargs, a.kw_defaults):
        parts.append(x.arg + ("=…" if d is not None else ""))
    if a.kwarg: parts.append("**" + a.kwarg.arg)
    return "(" + ", ".join(parts) + ")"
def bases(n):
    names = []
    for b in n.bases:
        if isinstance(b, ast.Name): names.append(b.id)
        elif isinstance(b, ast.Attribute): names.append(b.attr)
    return "(" + ", ".join(names) + ")" if names else ""
def doc(n):
    d = ast.get_docstring(n)
    return d.strip().splitlines()[0][:110] if d else ""
def start(n):
    ls = [n.lineno] + [d.lineno for d in getattr(n, "decorator_list", [])]
    return min(ls)
def walk(body, depth, scope, limit):
    for i, n in enumerate(body):
        nxt = start(body[i + 1]) - 1 if i + 1 < len(body) else limit
        end = getattr(n, "end_lineno", None) or max(nxt, n.lineno)
        if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)):
            out.append(dict(name=n.name, kind=("async def" if isinstance(n, ast.AsyncFunctionDef) else "def"), sig=sig(n), doc=doc(n), line=start(n), end=end, depth=depth, scope=scope))
        elif isinstance(n, ast.ClassDef):
            out.append(dict(name=n.name, kind="class", sig=bases(n), doc=doc(n), line=start(n), end=end, depth=depth, scope=scope))
            walk(n.body, depth + 1, (scope + "." if scope else "") + n.name, end)
        elif isinstance(n, (ast.Assign, ast.AnnAssign)) and depth <= 1:
            targets = n.targets if isinstance(n, ast.Assign) else [n.target]
            for t in targets:
                if isinstance(t, ast.Name):
                    out.append(dict(name=t.id, kind="var", sig="", doc="", line=n.lineno, end=end, depth=depth, scope=scope))
        elif isinstance(n, (ast.If, ast.Try)) and depth == 0:
            for blk in (n.body, getattr(n, "orelse", []), *[h.body for h in getattr(n, "handlers", [])]):
                walk(blk, depth, scope, limit)
walk(tree.body, 0, "", total)
print(json.dumps(out))
`

func pythonSymbols(ctx context.Context, o Options, abs string) ([]symbol, error) {
	py := ""
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			py = p
			break
		}
	}
	if py == "" {
		return nil, errors.New("no python on this machine")
	}
	ctx, cancel := context.WithTimeout(ctx, outlineTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, "-I", "-c", pythonScript, abs)
	cmd.Env = scrubEnv(o.EnvAllow)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("python could not parse %s: %s", filepath.Base(abs), strings.TrimSpace(lastLineOf(stderr.String())))
	}
	var syms []symbol
	if err := json.Unmarshal(stdout.Bytes(), &syms); err != nil {
		return nil, err
	}
	return syms, nil
}

func lastLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// ---- universal-ctags, then line patterns ----

func ctagsSymbols(ctx context.Context, o Options, abs string) ([]symbol, error) {
	ct, err := exec.LookPath("ctags")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, outlineTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ct, "--output-format=json", "--fields=+nKSse", "-o", "-", abs)
	cmd.Env = scrubEnv(o.EnvAllow)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var syms []symbol
	for _, l := range bytes.Split(stdout.Bytes(), []byte("\n")) {
		var t struct {
			Name      string `json:"name"`
			Kind      string `json:"kind"`
			Line      int    `json:"line"`
			End       int    `json:"end"`
			Scope     string `json:"scope"`
			Signature string `json:"signature"`
		}
		if json.Unmarshal(l, &t) != nil || t.Name == "" {
			continue
		}
		depth := 0
		if t.Scope != "" {
			depth = 1 + strings.Count(t.Scope, ".")
		}
		syms = append(syms, symbol{Name: t.Name, Kind: t.Kind, Sig: t.Signature, Line: t.Line, End: t.End, Depth: depth, Scope: t.Scope})
	}
	sort.SliceStable(syms, func(i, j int) bool { return syms[i].Line < syms[j].Line })
	return syms, nil
}

var patternRE = regexp.MustCompile(`^(\s*)(?:export\s+)?(?:default\s+)?(?:pub(?:\([a-z]+\))?\s+)?(?:async\s+)?(?:static\s+)?(def|class|function|func|fn|interface|struct|enum|trait|impl|type|module|const|let|var)\s+([A-Za-z_$][\w$]*)`)

func patternSymbols(src []byte) []symbol {
	var out []symbol
	lines := strings.Split(string(src), "\n")
	for i, l := range lines {
		m := patternRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		indent := len(strings.ReplaceAll(m[1], "\t", "    "))
		if (m[2] == "const" || m[2] == "let" || m[2] == "var") && indent > 0 {
			continue
		}
		out = append(out, symbol{Name: m[3], Kind: m[2], Line: i + 1, Depth: min(indent/4, 3)})
	}
	for i := range out {
		out[i].End = len(lines)
		for j := i + 1; j < len(out); j++ {
			if out[j].Depth <= out[i].Depth {
				out[i].End = out[j].Line - 1
				break
			}
		}
	}
	return out
}
