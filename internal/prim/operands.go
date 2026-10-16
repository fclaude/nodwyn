package prim

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// change-dropped-conditions names the parts of a condition that a change
// stopped checking: an operand of an && or || in the old body of a Go
// function the change rewrote, or the condition of a guard that returned
// early, that the function's new body no longer holds (ADR 32, amended
// 2026-10-15). On commit-bench (c80b38ac) dsl19's planner turned "the judge
// requires ready" into `result.Judge = (state == stateReady)`, though the old
// line it quoted read `s.options.Config.Jev.Enabled() && ...`; the executor
// saw the dropped check four times and followed the plan, and no test had a
// case where the judge's content was none, so every check passed. dsl16's
// planner made the same drop, and its executor put the operand back. The
// evidence loop reads this before an unattended turn ends on a passing
// check; it is advisory: a condition moved into a helper that the new body
// does not call reads as dropped.

const droppedMax = 3

// droppedOperand is a part of a condition the old body checked and the new
// one does not.
type droppedOperand struct {
	path, function, operand string
	guard                   bool // the whole condition of a guard that returned early
}

// goFunctions is the functions and methods of a Go file by name (Type.Method
// for a method), each with its body.
func goFunctions(fset *token.FileSet, f *ast.File) map[string]*ast.FuncDecl {
	out := map[string]*ast.FuncDecl{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		name := fd.Name.Name
		if fd.Recv != nil && len(fd.Recv.List) == 1 {
			name = receiverName(fd.Recv.List[0].Type) + "." + name
		}
		out[name] = fd
	}
	return out
}

// squashCode is source with its white space taken out, as two versions of an
// expression compare.
func squashCode(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// trivialOperand is an operand no note is worth: an error or ok check, a
// literal, or a name of one letter.
func trivialOperand(s string) bool {
	switch strings.TrimPrefix(s, "!") {
	case "err!=nil", "err==nil", "ok", "true", "false", "nil":
		return true
	}
	t := strings.TrimPrefix(s, "!")
	return len(t) < 2 || t[0] == '"' || t[0] >= '0' && t[0] <= '9'
}

// conditionParts is the operands of the && and || in a body, each the
// source of a leaf of such an expression, and the conditions of the ifs
// whose block ends in a return, whole (guards), unless they are an && or ||
// whose operands are read already.
func conditionParts(src []byte, fset *token.FileSet, body *ast.BlockStmt) (operands, guards []string) {
	text := func(e ast.Node) string {
		a, b := fset.Position(e.Pos()).Offset, fset.Position(e.End()).Offset
		if a < 0 || b > len(src) || a >= b {
			return ""
		}
		return string(src[a:b])
	}
	var leaves func(e ast.Expr)
	leaves = func(e ast.Expr) {
		switch x := e.(type) {
		case *ast.ParenExpr:
			leaves(x.X)
		case *ast.BinaryExpr:
			if x.Op == token.LAND || x.Op == token.LOR {
				leaves(x.X)
				leaves(x.Y)
				return
			}
			operands = append(operands, text(x))
		default:
			operands = append(operands, text(x))
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BinaryExpr:
			if x.Op == token.LAND || x.Op == token.LOR {
				leaves(x)
				return false
			}
		case *ast.IfStmt:
			if b, ok := ast.Unparen(x.Cond).(*ast.BinaryExpr); ok && (b.Op == token.LAND || b.Op == token.LOR) {
				break // its operands are read as any && or ||'s
			}
			if l := len(x.Body.List); l > 0 {
				if _, ret := x.Body.List[l-1].(*ast.ReturnStmt); ret {
					guards = append(guards, text(x.Cond))
				}
			}
		}
		return true
	})
	return operands, guards
}

// calledNames is the names a body calls: f for f(...) and x.f(...).
func calledNames(body *ast.BlockStmt) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			switch f := c.Fun.(type) {
			case *ast.Ident:
				out[f.Name] = true
			case *ast.SelectorExpr:
				out[f.Sel.Name] = true
			}
		}
		return true
	})
	return out
}

var (
	operandWordRE   = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*|"[^"]*"`)
	commonCodeWords = map[string]bool{
		"strings": true, "HasPrefix": true, "HasSuffix": true, "Contains": true, "TrimSpace": true, "filepath": true,
		"Clean": true, "errors": true, "options": true, "Config": true, "config": true, "string": true, "error": true,
		"bytes": true, "Equal": true, "Index": true, "slices": true, "self": true, "true": true, "false": true,
	}
)

// operandNamed is whether the request names a word of an operand: one of its
// names (Enabled, Kind) or the text of one of its string literals
// (Content-Type), of four characters or more and not a word every condition
// uses (strings, HasPrefix). A condition the request is about is one it may
// change: in the replay over the benchmark's original commits most of the
// dropped operands named what the task changed.
func operandNamed(request, operand string) bool {
	for _, w := range operandWordRE.FindAllString(operand, -1) {
		w = strings.Trim(w, `"`)
		if len(w) < 4 || commonCodeWords[w] {
			continue
		}
		if namedBy(request, w) {
			return true
		}
	}
	return false
}

// droppedOperands is the parts of conditions the change's rewritten Go
// functions no longer check: for each function in a changed non-test file
// whose body changed, the && and || operands and the guards' conditions of
// its old body found, white space aside and a leading ! set aside, neither in
// its new body nor in the body of a function of its package that it calls
// and the change added or rewrote (a condition moved into a new helper is
// still checked).
func droppedOperands(c changeFiles, request string) []droppedOperand {
	var out []droppedOperand
	for _, rel := range c.paths {
		if !strings.HasSuffix(rel, ".go") || testSide(rel) {
			continue
		}
		old, ok1 := c.head(rel)
		cur, ok2 := c.now(rel)
		if !ok1 || !ok2 {
			continue
		}
		ofs, nfs := token.NewFileSet(), token.NewFileSet()
		of, err1 := parser.ParseFile(ofs, rel, old, parser.SkipObjectResolution)
		nf, err2 := parser.ParseFile(nfs, rel, cur, parser.SkipObjectResolution)
		if err1 != nil || err2 != nil {
			continue
		}
		before, after := goFunctions(ofs, of), goFunctions(nfs, nf)
		body := func(src []byte, fset *token.FileSet, fd *ast.FuncDecl) string {
			return string(src[fset.Position(fd.Body.Pos()).Offset:fset.Position(fd.Body.End()).Offset])
		}
		// The bodies of the package's functions the change added or
		// rewrote, by name, for the helpers a new body may call.
		var helpers map[string]string
		names := make([]string, 0, len(after))
		for name := range after {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			nfd := after[name]
			ofd, kept := before[name]
			if !kept {
				continue
			}
			oldBody, newBody := body(old, ofs, ofd), body(cur, nfs, nfd)
			if squashCode(oldBody) == squashCode(newBody) {
				continue
			}
			if helpers == nil {
				helpers = changedHelpers(c, rel)
			}
			holds := squashCode(newBody)
			for callee := range calledNames(nfd.Body) {
				if h, ok := helpers[callee]; ok {
					holds += "\n" + h
				}
			}
			operands, guards := conditionParts(old, ofs, ofd.Body)
			seen := map[string]bool{}
			report := func(op string, guard bool) {
				s := squashCode(op)
				if s == "" || seen[s] || trivialOperand(s) || strings.Contains(holds, strings.TrimPrefix(s, "!")) || operandNamed(request, op) {
					return
				}
				seen[s] = true
				out = append(out, droppedOperand{path: rel, function: name, operand: strings.Join(strings.Fields(op), " "), guard: guard})
			}
			for _, op := range operands {
				report(op, false)
			}
			for _, g := range guards {
				report(g, true)
			}
		}
	}
	if len(out) > droppedMax {
		out = out[:droppedMax]
	}
	return out
}

// changedHelpers is the bodies, squashed, of the functions in rel's package
// that the change added or rewrote, by their own name (a method's without
// its type, as a call names it).
func changedHelpers(c changeFiles, rel string) map[string]string {
	out := map[string]string{}
	for _, p := range c.dir(path.Dir(rel)) {
		if !strings.HasSuffix(p, ".go") || testSide(p) {
			continue
		}
		cur, ok := c.now(p)
		if !ok {
			continue
		}
		nfs := token.NewFileSet()
		nf, err := parser.ParseFile(nfs, p, cur, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		var before map[string]*ast.FuncDecl
		ofs := token.NewFileSet()
		old, inHead := c.head(p)
		if inHead {
			if of, err := parser.ParseFile(ofs, p, old, parser.SkipObjectResolution); err == nil {
				before = goFunctions(ofs, of)
			}
		}
		for name, fd := range goFunctions(nfs, nf) {
			newBody := squashCode(string(cur[nfs.Position(fd.Body.Pos()).Offset:nfs.Position(fd.Body.End()).Offset]))
			if ofd, ok := before[name]; ok && squashCode(string(old[ofs.Position(ofd.Body.Pos()).Offset:ofs.Position(ofd.Body.End()).Offset])) == newBody {
				continue
			}
			out[name[strings.LastIndex(name, ".")+1:]] += newBody
		}
	}
	return out
}

func droppedOperandsPrim(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "change-dropped-conditions",
		Meta: scheme.Meta{
			Doc:    "The parts of conditions the uncommitted change stopped checking, at most " + fmt.Sprint(droppedMax) + ": for each Go function of a changed non-test file whose body changed, the operands of the && and || of its old body, and the conditions of its old guards (an if whose block ends in a return), that neither its new body holds (white space and a leading ! aside) nor the body of a function of its package it calls that the change added or rewrote; error and ok checks, literals and one-letter names left out. A list of alists of path, function (Type.Method for a method), operand and guard (#t for a guard's whole condition). Hidden; registered in a git work tree. The finish review names them when the latest run since the last edit passed (ADR 32, amended 2026-10-15).",
			Params: []scheme.Param{{Name: "request", Type: "string", Doc: "the request: an operand with a name it gives is not counted as dropped"}},
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
				return scheme.Value{}, fmt.Errorf("change-dropped-conditions: %w", err)
			}
			var out []scheme.Value
			for _, d := range droppedOperands(ch, request) {
				out = append(out, scheme.List(
					scheme.Cons(scheme.Str("path"), scheme.Str(d.path)),
					scheme.Cons(scheme.Str("function"), scheme.Str(d.function)),
					scheme.Cons(scheme.Str("operand"), scheme.Str(d.operand)),
					scheme.Cons(scheme.Str("guard"), scheme.Bool(d.guard))))
			}
			return scheme.List(out...), nil
		},
	}
}
