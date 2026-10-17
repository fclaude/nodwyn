package prim

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// tests-for answers "which tests exercise this file, and how do I run them":
// the test files that mention the most of what the file defines, rarest names
// counting most, each with the command that runs it where the repository's
// layout says. On the benchmark's Django sessions wrong test labels and
// working directories cost about 3 calls a session, as much as finding the
// interpreter, and the same wrong labels recurred across independent runs
// (`deconstruct`, `utils_tests.test_deconstruct`, `validators.test_validators`,
// `exists`); and a run that checked its change only against the tests it
// knew of missed the sibling that another one found (django-16938; ADR 32,
// amended 2026-10-17).
//
// It reads names, not dependencies: a test that imports the module and
// calls nothing it defines scores nothing, and a test that names a symbol
// without using the file's would score. The score is a ranking, not a proof.

const (
	testsForMaxFiles  = 12000
	testsForMaxBytes  = 512 << 10
	testsForShown     = 8
	testsForMaxNames  = 120
	testsForCommonCut = 0.35 // a name in more than this share of the test files says nothing
)

var identRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{3,}`)

// testsForStop are definitions every file has.
var testsForStop = map[string]bool{"self": true, "test": true, "tests": true, "main": true, "init": true, "setUp": true, "tearDown": true, "setup": true, "teardown": true, "None": true, "True": true, "False": true, "name": true, "value": true, "data": true, "args": true, "kwargs": true}

func testsForPrim(o Options, pin guard.RepoPin) *scheme.Prim {
	return &scheme.Prim{
		Name: "tests-for",
		Meta: scheme.Meta{
			Doc: "The test files that exercise a source file, and how to run them: it reads the file's definitions (outline), finds the test files (under test or tests folders, or named test_*, *_test.*, *.test.*, *.spec.*) that mention the most of them, the rarest names counting most, and prints each with the command that runs it where the repository's layout says (python tests/runtests.py <label> for a Django tree, pytest <path>, go test ./<dir>/). It ranks by names, it does not trace imports: use it to find where to look, then read the test.",
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
			text, err := testsFor(c.Ctx, o, pin, abs, c.Args[0].S)
			if err != nil {
				return scheme.Value{}, err
			}
			return scheme.Str(text), nil
		},
	}
}

func isTestFile(rel string) bool {
	return testPath(rel) || filepath.Base(rel) == "tests.py" || filepath.Base(rel) == "conftest.py"
}

type testHit struct {
	rel   string
	score float64
	names []string
}

func testsFor(ctx context.Context, o Options, pin guard.RepoPin, abs, shown string) (string, error) {
	syms, _, _, err := outlineSymbols(ctx, o, abs)
	if err != nil {
		return "", err
	}
	rel, _ := filepath.Rel(o.Root, abs)
	rel = filepath.ToSlash(rel)
	names := map[string]bool{}
	for _, s := range syms {
		if len(s.Name) >= 4 && !testsForStop[s.Name] && !strings.HasPrefix(s.Name, "__") && len(names) < testsForMaxNames {
			names[s.Name] = true
		}
	}
	// the module's own name, as an import line spells it
	base := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	if len(names) == 0 && len(base) < 4 {
		return "", fmt.Errorf("%s defines no name of four letters or more to search the tests for: search them with index-grep for the behaviour you changed", shown)
	}
	files, err := searchFilesWith(ctx, o.Root, o.Root, "", testsForMaxFiles, func(name string) bool {
		r, e := filepath.Rel(o.Root, name)
		return e == nil && isTestFile(filepath.ToSlash(r)) && isSourceFile(name)
	})
	if err != nil {
		return "", fmt.Errorf("too many test files to search: %w", err)
	}
	type seen struct {
		rel     string
		matched []string
		module  bool
	}
	var found []seen
	df := map[string]int{}
	deny := denylist(o)
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		r, _ := filepath.Rel(o.Root, f)
		r = filepath.ToSlash(r)
		if r == rel || !keepable(deny, r) {
			continue
		}
		st, err := os.Stat(f)
		if err != nil || st.Size() > testsForMaxBytes {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		text := string(b)
		set := map[string]bool{}
		for _, id := range identRE.FindAllString(text, -1) {
			if names[id] {
				set[id] = true
			}
		}
		module := len(base) >= 4 && strings.Contains(text, base) && (strings.Contains(text, "import") || strings.Contains(text, "require") || strings.Contains(text, "use "))
		if len(set) == 0 && !module {
			continue
		}
		var m []string
		for id := range set {
			m = append(m, id)
			df[id]++
		}
		found = append(found, seen{rel: r, matched: m, module: module})
	}
	n := float64(len(files))
	var hits []testHit
	for _, f := range found {
		score := 0.0
		var kept []string
		for _, id := range f.matched {
			if float64(df[id]) > testsForCommonCut*n && n > 20 {
				continue
			}
			score += math.Log(1 + n/float64(1+df[id]))
			kept = append(kept, id)
		}
		if f.module {
			score += 3
		}
		if score <= 0 {
			continue
		}
		sort.Slice(kept, func(i, j int) bool {
			return df[kept[i]] < df[kept[j]] || (df[kept[i]] == df[kept[j]] && kept[i] < kept[j])
		})
		hits = append(hits, testHit{rel: f.rel, score: score, names: kept})
	}
	sort.Slice(hits, func(i, j int) bool {
		return hits[i].score > hits[j].score || (hits[i].score == hits[j].score && hits[i].rel < hits[j].rel)
	})
	var b strings.Builder
	fmt.Fprintf(&b, "tests that mention what %s defines (%d of %d test files mention at least one name):\n", shown, len(hits), len(files))
	if len(hits) == 0 {
		b.WriteString("none found by name: search the tests with index-grep for the behaviour you changed\n")
		return b.String(), nil
	}
	for i, h := range hits {
		if i >= testsForShown {
			fmt.Fprintf(&b, "… %d more\n", len(hits)-i)
			break
		}
		shownNames := h.names
		if len(shownNames) > 5 {
			shownNames = append(shownNames[:5:5], fmt.Sprintf("+%d", len(h.names)-5))
		}
		fmt.Fprintf(&b, "%d. %s  (%s)\n", i+1, h.rel, strings.Join(shownNames, ", "))
		if i < 3 {
			if cmd := testRunCommand(o.Root, h.rel); cmd != "" {
				fmt.Fprintf(&b, "   run: %s\n", cmd)
			}
		}
	}
	return b.String(), nil
}

// testRunCommand is the command that runs a test file where the repository's
// own layout says how: a tests/runtests.py is Django's, a go file runs by its
// directory, a Python file by pytest when the tree is configured for it.
func testRunCommand(root, rel string) string {
	exists := func(p string) bool { _, err := os.Stat(filepath.Join(root, p)); return err == nil }
	switch ext := strings.ToLower(filepath.Ext(rel)); {
	case ext == ".go":
		if d := filepath.ToSlash(filepath.Dir(rel)); d != "." {
			return "go test ./" + d + "/"
		}
		return "go test ."
	case ext == ".py" && exists("tests/runtests.py") && strings.HasPrefix(rel, "tests/"):
		label := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(rel, "tests/"), ".py"), "/", ".")
		return "python tests/runtests.py " + label + "   (from the repository root; use the project's interpreter)"
	case ext == ".py":
		return "python -m pytest " + rel
	case ext == ".js" || ext == ".ts" || ext == ".jsx" || ext == ".tsx":
		return "npm test -- " + rel
	}
	return ""
}
