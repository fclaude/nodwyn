package prim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// A plan can be written as Scheme data, one form per step (planspec.scm):
// what the plan names, the files, the tests and the commands that run them,
// and the names the change must produce, are then fields the harness reads
// instead of words in a sentence. These primitives are its host side: read
// the data without running it, take the names a request gives, and set a
// plan's names and files against the change as git shows it (ADR 32,
// amended 2026-10-13).

// readData builds (read-data text): every datum in text, unevaluated.
func readData() *scheme.Prim {
	return &scheme.Prim{
		Name: "read-data",
		Meta: scheme.Meta{
			Doc:    "Read every datum in text as data, without evaluating it: a list of the forms, strings, symbols and numbers it holds. An error names where text stops being readable.",
			Params: []scheme.Param{{Name: "text", Type: "string", Doc: "the text to read"}},
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			text, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			vs, err := scheme.Read(text)
			if err != nil {
				return scheme.Value{}, fmt.Errorf("read-data: %w", err)
			}
			return scheme.List(vs...), nil
		},
	}
}

var (
	requestNameRE = regexp.MustCompile("`([^`\\n]{2,60})`|\\b([A-Za-z][A-Za-z0-9]*(?:[-_.][A-Za-z0-9]+)+|[a-z]+[A-Z][A-Za-z0-9]*|[A-Z][a-z0-9]+[A-Z][A-Za-z0-9]*)\\b")
	issueRefRE    = regexp.MustCompile(`#\d+$`)
	linkRE        = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://\S+`)
	versionRE     = regexp.MustCompile(`^v?\d+([._-]\d+)*$`)
)

// requestNames is the names a request gives: spans in backquotes without a
// space, and words that read as names, with a hyphen, an underscore or a dot
// inside or a capital after the first letter (session-id, cache_key,
// response.output_item.done, readResponses, JevStored). Not a link, an issue
// number or a version.
func requestNames(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range requestNameRE.FindAllStringSubmatch(linkRE.ReplaceAllString(text, " "), -1) {
		s := strings.TrimSpace(m[1])
		if s == "" {
			s = m[2]
		}
		if s == "" || strings.ContainsAny(s, " \t") || strings.Contains(s, "://") || strings.HasPrefix(s, "www.") ||
			issueRefRE.MatchString(s) || versionRE.MatchString(s) || s == "e.g" || s == "i.e" {
			continue
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func requestNamesPrim() *scheme.Prim {
	return &scheme.Prim{
		Name: "request-names",
		Meta: scheme.Meta{
			Doc:    "The names a request gives, in order, each once: spans in backquotes without a space, and words that read as names (with a hyphen, an underscore or a dot inside, or a capital after the first letter: session-id, cache_key, readResponses). Not links, issue numbers or versions.",
			Params: []scheme.Param{{Name: "text", Type: "string", Doc: "the request"}},
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			text, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			names := requestNames(text)
			out := make([]scheme.Value, len(names))
			for i, n := range names {
				out[i] = scheme.Str(n)
			}
			return scheme.List(out...), nil
		},
	}
}

// hasName is whether name is in text as a whole name, in any case: the
// characters around it are not letters, digits, an underscore or a hyphen,
// so session-id is not found in X-Session-Id.
func hasName(text, name string) bool {
	if name == "" {
		return false
	}
	lt, ln := strings.ToLower(text), strings.ToLower(name)
	part := func(b byte) bool {
		return b == '_' || b == '-' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z'
	}
	for from := 0; ; {
		i := strings.Index(lt[from:], ln)
		if i < 0 {
			return false
		}
		i += from
		j := i + len(ln)
		if (i == 0 || !part(lt[i-1])) && (j == len(lt) || !part(lt[j])) {
			return true
		}
		from = i + 1
	}
}

// changeText is what the uncommitted change adds: the added lines of the
// tracked files' diff against HEAD, and the whole of each new file, each
// cut at withoutMaxFile; and the paths it changes.
func changeText(ctx context.Context, o Options) (string, map[string]bool, error) {
	k := &Keeper{o: o}
	_, paths, err := k.look(ctx)
	if err != nil {
		return "", nil, err
	}
	changed := map[string]bool{}
	for _, p := range paths {
		changed[filepath.ToSlash(filepath.Clean(p))] = true
	}
	diff, err := runGitLimit(ctx, o, 16<<20, "diff", "HEAD", "--no-color", "--no-ext-diff", "-U0")
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			b.WriteString(line[1:])
			b.WriteByte('\n')
		}
	}
	tracked, err := k.headFiles(ctx)
	if err != nil {
		return "", nil, err
	}
	for _, p := range paths {
		if _, inHead := tracked[p]; inHead {
			continue
		}
		abs := filepath.Join(o.Root, filepath.FromSlash(p))
		if info, err := os.Lstat(abs); err != nil || !info.Mode().IsRegular() || info.Size() > withoutMaxFile {
			continue
		}
		data, err := os.ReadFile(abs)
		if err == nil {
			b.Write(data)
			b.WriteByte('\n')
		}
	}
	return b.String(), changed, nil
}

// planEvidence builds (plan-evidence names paths): the names the change does
// not contain and the paths it does not change.
func planEvidence(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "plan-evidence",
		Meta: scheme.Meta{
			Doc:    "Set a plan against the uncommitted change as git shows it: an alist of missing, the names the change does not add (a name is found whole and in any case in the added lines and new files: session-id is not found in X-Session-Id), and unchanged, the paths it does not change. Registered in a git work tree.",
			Params: []scheme.Param{{Name: "names", Type: "list", Doc: "names the change must add"}, {Name: "paths", Type: "list", Doc: "repository paths the change must touch"}},
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			strs := func(i int) ([]string, error) {
				items, err := c.List(i)
				if err != nil {
					return nil, err
				}
				out := make([]string, 0, len(items))
				for _, it := range items {
					if it.Kind != scheme.KString {
						return nil, errors.New("plan-evidence takes lists of strings")
					}
					out = append(out, it.S)
				}
				return out, nil
			}
			names, err := strs(0)
			if err != nil {
				return scheme.Value{}, err
			}
			paths, err := strs(1)
			if err != nil {
				return scheme.Value{}, err
			}
			text, changed, err := changeText(c.Ctx, o)
			if err != nil {
				return scheme.Value{}, fmt.Errorf("plan-evidence: %w", err)
			}
			var missing, unchanged []scheme.Value
			for _, n := range names {
				if !hasName(text, n) {
					missing = append(missing, scheme.Str(n))
				}
			}
			for _, p := range paths {
				clean := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(p, "./")))
				if !changed[clean] {
					unchanged = append(unchanged, scheme.Str(p))
				}
			}
			return scheme.List(
				scheme.Cons(scheme.Str("missing"), scheme.List(missing...)),
				scheme.Cons(scheme.Str("unchanged"), scheme.List(unchanged...)),
			), nil
		},
	}
}
