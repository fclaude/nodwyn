package prim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fclaude/nodwyn/internal/guard"
)

// The code-index tools as commands (nodwyn outline FILE, nodwyn symbol FILE
// NAME, nodwyn tests-for FILE): the same readers as the outline and tests-for
// tools and fs-read's symbol option, run from a shell with the repository as
// the working directory. A model that already runs everything through
// shell-exec reaches them without a tool of its own in its list, which is
// the leanest way to offer them: no schema in every request, one line in the
// prompt.

func cliRoot() (Options, error) {
	wd, err := os.Getwd()
	if err != nil {
		return Options{}, err
	}
	return DefaultOptions(wd), nil
}

func cliPath(o Options, name string) (string, error) {
	abs := name
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(o.Root, name)
	}
	abs = filepath.Clean(abs)
	if rel, err := filepath.Rel(o.Root, abs); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the working directory", name)
	}
	return abs, nil
}

// OutlineCommand prints a file's outline.
func OutlineCommand(ctx context.Context, name string) (string, error) {
	o, err := cliRoot()
	if err != nil {
		return "", err
	}
	abs, err := cliPath(o, name)
	if err != nil {
		return "", err
	}
	return outlineText(ctx, o, abs, name)
}

// SymbolCommand prints one definition of a file with line numbers.
func SymbolCommand(ctx context.Context, name, symbol string) (string, error) {
	o, err := cliRoot()
	if err != nil {
		return "", err
	}
	abs, err := cliPath(o, name)
	if err != nil {
		return "", err
	}
	syms, _, _, err := outlineSymbols(ctx, o, abs)
	if err != nil {
		return "", err
	}
	sym, err := findSymbol(syms, symbol)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	end := min(sym.End, len(lines))
	var out strings.Builder
	head := sym.Kind + " " + sym.qualified()
	if sym.Scope != "" {
		head += " (in " + sym.Scope + ")"
	}
	fmt.Fprintf(&out, "%s: %s, lines %d-%d of %d\n", name, head, sym.Line, end, len(lines))
	for i := sym.Line; i <= end && i-sym.Line < 400; i++ {
		fmt.Fprintf(&out, "%d\t%s\n", i, lines[i-1])
	}
	if end-sym.Line >= 400 {
		fmt.Fprintf(&out, "… %d more lines: read the rest with sed -n '%d,%dp' %s\n", end-sym.Line-399, sym.Line+400, end, name)
	}
	return out.String(), nil
}

// TestsForCommand prints the test files that exercise a source file.
func TestsForCommand(ctx context.Context, name string) (string, error) {
	o, err := cliRoot()
	if err != nil {
		return "", err
	}
	abs, err := cliPath(o, name)
	if err != nil {
		return "", err
	}
	return testsFor(ctx, o, guard.RepoPin{Root: o.Root}, abs, name)
}
