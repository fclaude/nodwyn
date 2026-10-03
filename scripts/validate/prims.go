package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
)

// newPrimitives lists the Scheme primitives the change adds: the names of
// scheme.Prim literals in the changed host files that the same files did
// not have at base. A primitive moved from one changed file to another is
// not new.
func newPrimitives(base string, changed []string) ([]string, error) {
	before, after := map[string]bool{}, map[string]bool{}
	for _, p := range changed {
		if !strings.HasPrefix(p, "internal/") || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			continue
		}
		if src, err := git("show", base+":"+p); err == nil {
			primNames(src, before)
		}
		if raw, err := os.ReadFile(p); err == nil {
			primNames(string(raw), after)
		}
	}
	var out []string
	for n := range after {
		if !before[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// primNames adds the Name of every scheme.Prim literal in src to into,
// including the elements of a []*scheme.Prim whose type is left out.
func primNames(src string, into map[string]bool) {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, 0)
	if err != nil {
		return
	}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if isPrim(lit.Type) {
			addName(lit, into)
		}
		if arr, ok := lit.Type.(*ast.ArrayType); ok && isPrim(arr.Elt) {
			for _, e := range lit.Elts {
				if u, ok := e.(*ast.UnaryExpr); ok {
					e = u.X
				}
				if el, ok := e.(*ast.CompositeLit); ok && el.Type == nil {
					addName(el, into)
				}
			}
		}
		return true
	})
}

func isPrim(t ast.Expr) bool {
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	sel, ok := t.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Prim" {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "scheme"
}

func addName(lit *ast.CompositeLit, into map[string]bool) {
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "Name" {
			continue
		}
		if s, ok := kv.Value.(*ast.BasicLit); ok && s.Kind == token.STRING {
			if name, err := strconv.Unquote(s.Value); err == nil {
				into[name] = true
			}
		}
	}
}
