package prim

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// scheme offers the model the language the harness is written in (the lean
// ladder's rung 4, 2026-10-16): a small program that calls the other tools as
// procedures, to read, search and edit several places and filter what comes
// back in one call instead of one call each. dsl23's main loop made 2,352
// requests for 121 tasks where Pi made 2,431 for everything, and a request
// sends the whole context again.
//
// It is no new authority. The program runs with the model's origin, so every
// tool it calls goes through the guard, the repository pin, the denylist and
// the approvals as that call would alone; the program is read as data and
// run in a frame of its own that holds only the tools the model is offered
// (but this one, so no program starts another) and the language's pure
// builtins (not register-tool, which would add a tool, nor the strategy's
// hidden primitives, nor any procedure a strategy defined: a frame built
// from the allowed names alone has no others to find, and the language has no
// eval or environment lookup by name). It is bounded in time and in output.

// schemeToolBudget is the time a program has; each tool it calls also keeps
// its own limits.
var schemeToolBudget = 3 * time.Minute

// schemeToolOutputMax is the longest result a program returns, in bytes.
const schemeToolOutputMax = 24 << 10

// schemeToolDoc is what the model reads of the tool.
const schemeToolDoc = "Run a small Scheme program that calls the other tools as procedures, to do several reads, searches, edits or commands in one call and keep only what matters of the results. A tool is a procedure that takes its parameters positionally, in the order its schema lists them (optional ones may be left off at the end); a list is (list \"a\" \"b\"). The value of the last expression is the result. Allowed: define, let, let*, lambda, if, cond, begin, and, or, guard, map, for-each, filter, fold, sort, apply, append, reverse, list-ref, cons, car, cdr, length, string-append, string-contains?, string-prefix?, string-suffix?, string-split, string-join, string-replace, substring, number->string, string->number, format, + - * / and the comparisons. A tool that fails raises an error; (guard (e (else \"failed\")) (fs-read \"x\")) keeps going. Every call goes through the same checks as a call of its own. Examples: (map (lambda (p) (fs-read p 1 40)) (list \"a.py\" \"b.py\")); (filter (lambda (l) (string-contains? l \"TODO\")) (string-split (fs-read \"a.py\") \"\\n\")); (shell-exec \"bash\" (list \"-c\" \"python -m pytest -x -q 2>&1 | tail -n 20\"))"

func schemeTool() *scheme.Prim {
	return &scheme.Prim{
		Name: "scheme",
		Meta: scheme.Meta{
			Doc:    schemeToolDoc,
			Params: []scheme.Param{{Name: "code", Type: "string", Doc: "the program: one or more Scheme expressions"}},
			Host:   true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			code, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			return runSchemeTool(c.Ctx, c.In, code)
		},
	}
}

// schemeAllowed is whether a program may name p: a tool but this one, or a
// pure builtin of the language.
func schemeAllowed(p *scheme.Prim) bool {
	if p.Name == "scheme" || p.Name == "register-tool" {
		return false
	}
	if p.Tool() {
		return true
	}
	return p.Meta.Origin == "builtin" && !p.Meta.Hidden && !p.Meta.Host
}

func runSchemeTool(parent context.Context, in *scheme.Interp, code string) (scheme.Value, error) {
	forms, err := scheme.Read(code)
	if err != nil {
		return scheme.Value{}, fmt.Errorf("scheme: %w", err)
	}
	if len(forms) == 0 {
		return scheme.Value{}, errors.New("scheme: the program is empty")
	}
	env := scheme.NewEnv(nil)
	for _, p := range in.Prims() {
		if schemeAllowed(p) {
			env.Define(p.Name, scheme.Value{Kind: scheme.KPrim, Prim: p})
		}
	}
	// A name that is a primitive but not allowed is said, instead of as an
	// unbound variable: the model should know it is not offered, not that it
	// misspelt it.
	if bad := schemeNotAllowed(in, forms); bad != "" {
		return scheme.Value{}, fmt.Errorf("scheme: %s is not available here; only the tools you are offered and the language's own procedures are", bad)
	}
	ctx, cancel := context.WithTimeout(parent, schemeToolBudget)
	defer cancel()
	// What a program computes with is whole: shell-exec shows the model a view
	// of a long output (shellview.go), not a program that splits and filters it.
	ctx = context.WithValue(ctx, nestedKey{}, true)
	var last scheme.Value = scheme.Void
	for _, f := range forms {
		v, err := in.Eval(ctx, f, env)
		if err != nil {
			if ctx.Err() != nil && parent.Err() == nil {
				return scheme.Value{}, fmt.Errorf("scheme: out of time after %s", schemeToolBudget)
			}
			return scheme.Value{}, err
		}
		last = v
	}
	if out := scheme.Write(last); len(out) > schemeToolOutputMax && last.Kind != scheme.KString {
		return scheme.Str(out[:schemeToolOutputMax] + fmt.Sprintf("\n[result truncated at %d KiB]", schemeToolOutputMax>>10)), nil
	}
	if last.Kind == scheme.KString && len(last.S) > schemeToolOutputMax {
		cut := last
		cut.S = last.S[:schemeToolOutputMax] + fmt.Sprintf("\n[result truncated at %d KiB]", schemeToolOutputMax>>10)
		return cut, nil
	}
	return last, nil
}

// schemeNotAllowed is the first symbol of the program that names a primitive
// a program may not use, or "".
func schemeNotAllowed(in *scheme.Interp, forms []scheme.Value) string {
	var walk func(v scheme.Value) string
	walk = func(v scheme.Value) string {
		switch v.Kind {
		case scheme.KSymbol:
			if p, ok := in.Lookup(v.S); ok && !schemeAllowed(p) {
				return v.S
			}
		case scheme.KPair:
			for cur := v; cur.Kind == scheme.KPair; cur = cur.P.Cdr {
				if s := walk(cur.P.Car); s != "" {
					return s
				}
				if cur.P.Cdr.Kind != scheme.KPair && cur.P.Cdr.Kind != scheme.KNil {
					if s := walk(cur.P.Cdr); s != "" {
						return s
					}
				}
			}
		case scheme.KVec:
			for _, e := range v.V.Items {
				if s := walk(e); s != "" {
					return s
				}
			}
		}
		return ""
	}
	for _, f := range forms {
		if s := walk(f); s != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
