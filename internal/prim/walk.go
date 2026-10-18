package prim

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// searchFiles lists the regular files under path, in path order, as
// index-grep and fs-find see them: names only, nothing opened. It walks the
// tree in Go, so search needs no program on PATH. Hidden entries below path
// (a name starting with a dot, .git included) and symbolic links are never
// listed or followed, and no ignore file is read: an ignore file could
// itself link to protected content. glob, if not "", keeps the paths it
// matches (see compileGlob); limit bounds the candidates, 0 for no bound.
// A path that is a file is returned as itself.
func searchFiles(ctx context.Context, root, path, glob string, limit int) ([]string, error) {
	return searchFilesWith(ctx, root, path, glob, limit, nil)
}

// isSourceFile is whether a file's extension is one of a programming
// language's (expectationLanguages) or a build or configuration file a code
// search is most often after.
func isSourceFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return slices.Contains(expectationLanguages, ext) || slices.Contains([]string{".cfg", ".toml", ".ini", ".yml", ".yaml", ".mod", ".gradle", ".pyi", ".pyx"}, ext)
}

// searchFilesWith is searchFiles with a further test every candidate must
// pass (nil for none).
func searchFilesWith(ctx context.Context, root, path, glob string, limit int, keep func(name string) bool) ([]string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []string{path}, nil
	}
	var match func(string) bool
	if glob != "" {
		if match, err = compileGlob(glob); err != nil {
			return nil, err
		}
	}
	var files []string
	count := 0
	err = filepath.WalkDir(path, func(name string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			if name == path {
				return err
			}
			return nil // an unreadable entry is not a candidate
		}
		if name == path {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		if match != nil && !globMatches(match, root, path, name) {
			return nil
		}
		if keep != nil && !keep(name) {
			return nil
		}
		count++
		if limit > 0 && count > limit {
			return errTooMany
		}
		files = append(files, name)
		return nil
	})
	if err == errTooMany {
		return nil, fmt.Errorf("search found more than %d candidate files; narrow the path, or pass a glob such as *.py", limit)
	}
	if err != nil {
		return nil, err
	}
	return files, nil
}

var errTooMany = fmt.Errorf("too many candidate files")

// globMatches tries a file's path relative to the directory searched and
// relative to the repository root, so "migrations/*.py" under "django" and
// "django/**/migrations/*.py" from "." both find the same files.
func globMatches(match func(string) bool, root, dir, name string) bool {
	for _, base := range []string{dir, root} {
		if rel, err := filepath.Rel(base, name); err == nil && !strings.HasPrefix(rel, "..") {
			if match(filepath.ToSlash(rel)) {
				return true
			}
		}
	}
	return false
}

// compileGlob turns a gitignore-style glob, as ripgrep's --glob takes, into
// a matcher over slash-separated relative paths:
//
//   - *, ? and [...] match within one path component; ** matches any number
//     of components; {a,b} matches either alternative;
//   - a glob without a slash matches any component of the path, so *.py
//     finds a.py at any depth and tests finds every file under a tests
//     directory;
//   - a glob with a slash matches from the start of the path (a leading / is
//     only an anchor), and matching a directory matches the files under it;
//   - a leading ! inverts the glob: every path it does not match.
func compileGlob(glob string) (func(string) bool, error) {
	negate := strings.HasPrefix(glob, "!")
	glob = strings.TrimPrefix(glob, "!")
	glob = strings.TrimSuffix(glob, "/")
	if glob == "" {
		return nil, fmt.Errorf("glob %q matches nothing", "!")
	}
	anchored := strings.Contains(strings.TrimPrefix(glob, "/"), "/") || strings.HasPrefix(glob, "/")
	body, err := globRegexp(strings.TrimPrefix(glob, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid glob %q: %w", glob, err)
	}
	var re *regexp.Regexp
	if anchored {
		// The path itself, or a directory above the file.
		re = regexp.MustCompile("^" + body + "(?:/.*)?$")
	} else {
		// Any component: at the start or after a slash, up to a slash or the end.
		re = regexp.MustCompile("(?:^|/)" + body + "(?:/|$)")
	}
	return func(p string) bool { return re.MatchString(p) != negate }, nil
}

// globRegexp translates glob syntax into a regular expression body.
func globRegexp(glob string) (string, error) {
	var b strings.Builder
	depth := 0 // inside {…}
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '*' && strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case c == '*' && strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				return "", fmt.Errorf("unclosed [")
			}
			class := glob[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		case c == '{':
			depth++
			b.WriteString("(?:")
		case c == '}' && depth > 0:
			depth--
			b.WriteString(")")
		case c == ',' && depth > 0:
			b.WriteString("|")
		case c == '\\' && i+1 < len(glob):
			i++
			b.WriteString(regexp.QuoteMeta(string(glob[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if depth != 0 {
		return "", fmt.Errorf("unclosed {")
	}
	if _, err := regexp.Compile(b.String()); err != nil {
		return "", err
	}
	return b.String(), nil
}
