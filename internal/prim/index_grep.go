package prim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// Every candidate file is one guarded fs-read, and each read's guard
// decision is logged, so the limit also bounds what one search adds to the
// log; a glob narrows a large tree to the files that matter.
var searchFileLimit = 3000

const searchMatchLimit = 100
const searchContextLimit = 5
const findLimit = 500
const searchTimeout = 30 * time.Second

type searchMatch struct {
	Path   string   `json:"path"`
	Line   int      `json:"line"`
	Text   string   `json:"text"`
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
}

type searchResult struct {
	Matches   []searchMatch `json:"matches"`
	Files     int           `json:"files"`
	Skipped   int           `json:"skipped"`
	Truncated bool          `json:"truncated"`
	Note      string        `json:"note,omitempty"`
}

func indexGrep(o Options, pin guard.RepoPin) *scheme.Prim {
	return &scheme.Prim{
		Name: "index-grep",
		Meta: scheme.Meta{
			Doc: "Search the contents of a repository file or directory with a regular expression (RE2 syntax, as in Go and ripgrep). Returns matches (path, line, text, and with context the lines before and after), files searched, files skipped and a truncated flag. At most 3000 candidate files and 100 matching lines: in a larger tree only source and configuration files are searched (a note says so); narrow the path or pass a glob such as *.py or docs/**/*.txt to choose. Hidden files, protected files, symlinks, binary and oversized files are skipped.",
			Params: []scheme.Param{
				{Name: "path", Type: "string", Doc: "repository file or directory", Role: "path"},
				{Name: "pattern", Type: "string", Doc: "regular expression, not command-line options"},
				{Name: "glob", Type: "string", Doc: "only files whose path matches this glob, such as *.py or tests/**/*.py; \"\" for all", Optional: true},
				{Name: "context", Type: "integer", Doc: "lines of context before and after each match, at most 5", Optional: true},
			},
			Host: true, TaintSource: scheme.TagFile,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			path, err := resolve(pin, c, 0)
			if err != nil {
				return scheme.Value{}, err
			}
			pattern, err := c.String(1)
			if err != nil {
				return scheme.Value{}, err
			}
			if len(pattern) > 4096 {
				return scheme.Value{}, fmt.Errorf("pattern exceeds 4096 bytes")
			}
			// Validated even when discovery returns no searchable files.
			re, err := regexp.Compile(pattern)
			if err != nil {
				return scheme.Value{}, fmt.Errorf("invalid regular expression: %w", err)
			}
			glob := ""
			if len(c.Args) > 2 {
				if glob, err = c.String(2); err != nil {
					return scheme.Value{}, err
				}
			}
			around := 0
			if len(c.Args) > 3 {
				n, err := c.Int(3)
				if err != nil {
					return scheme.Value{}, err
				}
				if n < 0 {
					return scheme.Value{}, fmt.Errorf("context is 0 to %d lines, got %d", searchContextLimit, n)
				}
				around = int(min(n, searchContextLimit)) // more is capped, not refused
			}
			ctx, cancel := context.WithTimeout(c.Ctx, searchTimeout)
			defer cancel()
			files, err := searchFiles(ctx, o.Root, path, glob, searchFileLimit)
			note := ""
			if err != nil && glob == "" && strings.Contains(err.Error(), "candidate files") {
				// Refused until 2026-10-17: 33 of 748 calls in the benchmark
				// transcripts, each a wasted call and the model's next one a
				// glob it could have been spared. Source files are what a code
				// search is after; a glob widens it again.
				if files, err = searchFilesWith(ctx, o.Root, path, glob, searchFileLimit, isSourceFile); err == nil {
					note = fmt.Sprintf("the tree has more than %d files, so only source and configuration files were searched; pass a glob (such as *.rst or docs/**/*.txt) to search others", searchFileLimit)
				}
			}
			if err != nil {
				return scheme.Value{}, err
			}
			read, ok := c.In.Lookup("fs-read")
			if !ok {
				return scheme.Value{}, fmt.Errorf("fs-read is unavailable")
			}
			deny := denylist(o)
			result := searchResult{Matches: []searchMatch{}}
			outputBytes := 0
			for _, file := range files {
				if err := ctx.Err(); err != nil {
					return scheme.Value{}, err
				}
				if !searchableFile(ctx, pin, deny, read, file, o.MaxReadBytes) {
					result.Skipped++
					continue
				}
				// Nested reads take the same single guarded application path as direct
				// model calls, including user-defined checkers. Denials are never
				// swallowed; a file that cannot be read is skipped.
				value, err := c.In.Apply(ctx, scheme.Value{Kind: scheme.KPrim, Prim: read}, []scheme.Value{scheme.Str(file)})
				if err != nil && scheme.Catchable(ctx, err) {
					result.Skipped++
					continue
				}
				if err != nil {
					return scheme.Value{}, err
				}
				if !utf8.ValidString(value.S) || strings.ContainsRune(value.S, 0) {
					result.Skipped++
					continue
				}
				result.Files++
				if value.S == "" {
					continue
				}
				relative, err := filepath.Rel(o.Root, file)
				if err != nil {
					return scheme.Value{}, err
				}
				lines := strings.Split(strings.TrimSuffix(value.S, "\n"), "\n")
				for n, line := range lines {
					if !re.MatchString(line) {
						continue
					}
					m := searchMatch{Path: filepath.ToSlash(relative), Line: n + 1, Text: strings.TrimSuffix(line, "\r")}
					size := len(m.Path) + len(m.Text) + 32
					if around > 0 {
						for k := max(0, n-around); k < n; k++ {
							m.Before = append(m.Before, strings.TrimSuffix(lines[k], "\r"))
							size += len(lines[k]) + 4
						}
						for k := n + 1; k < len(lines) && k <= n+around; k++ {
							m.After = append(m.After, strings.TrimSuffix(lines[k], "\r"))
							size += len(lines[k]) + 4
						}
					}
					outputBytes += size
					if len(result.Matches) == searchMatchLimit || outputBytes > o.MaxOutput {
						result.Truncated = true
						break
					}
					result.Matches = append(result.Matches, m)
				}
				if result.Truncated {
					break
				}
			}
			result.Note = note
			raw, err := json.Marshal(result)
			if err != nil {
				return scheme.Value{}, err
			}
			return scheme.FromJSON(raw)
		},
	}
}

type findResult struct {
	Files     []string `json:"files"`
	Count     int      `json:"count"`
	Truncated bool     `json:"truncated"`
}

// fsFind lists file names under a directory by glob, the way index-grep
// finds its candidates: names only, no contents read, protected and
// credential-looking names left out.
func fsFind(o Options, pin guard.RepoPin) *scheme.Prim {
	return &scheme.Prim{
		Name: "fs-find",
		Meta: scheme.Meta{
			Doc: "Find files under a repository directory whose path matches a glob, such as *.py, test_*.py or django/**/migrations/*.py. Returns sorted paths relative to the repository root (at most 500), their count and a truncated flag. Names only: read a file with fs-read, search contents with index-grep.",
			Params: []scheme.Param{
				{Name: "path", Type: "string", Doc: "repository directory to search under", Role: "path"},
				{Name: "glob", Type: "string", Doc: "glob the file's path must match"},
			},
			Host: true, TaintSource: scheme.TagFile,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			path, err := resolve(pin, c, 0)
			if err != nil {
				return scheme.Value{}, err
			}
			glob, err := c.String(1)
			if err != nil {
				return scheme.Value{}, err
			}
			if strings.TrimSpace(glob) == "" {
				return scheme.Value{}, errors.New("give a glob, such as *.py")
			}
			if st, err := os.Stat(path); err != nil {
				return scheme.Value{}, err
			} else if !st.IsDir() {
				return scheme.Value{}, fmt.Errorf("%s is not a directory", c.Args[0].S)
			}
			ctx, cancel := context.WithTimeout(c.Ctx, searchTimeout)
			defer cancel()
			files, err := searchFiles(ctx, o.Root, path, glob, 0)
			if err != nil {
				return scheme.Value{}, err
			}
			deny := denylist(o)
			read, _ := c.In.Lookup("fs-read")
			result := findResult{Files: []string{}}
			for _, file := range files {
				if !utf8.ValidString(file) || pin.Resolve(file) == "" ||
					(read != nil && deny.Check(ctx, read, []scheme.Value{scheme.Str(file)}).Verdict == guard.Deny) {
					continue
				}
				result.Count++
				if len(result.Files) == findLimit {
					result.Truncated = true
					continue
				}
				relative, err := filepath.Rel(o.Root, file)
				if err != nil {
					return scheme.Value{}, err
				}
				result.Files = append(result.Files, filepath.ToSlash(relative))
			}
			raw, err := json.Marshal(result)
			if err != nil {
				return scheme.Value{}, err
			}
			return scheme.FromJSON(raw)
		},
	}
}

// Apply the built-in exclusions before reading content. The subsequent fs-read
// still evaluates the complete guard, so this is filtering, not authorization.
func searchableFile(ctx context.Context, pin guard.RepoPin, deny guard.PathDenylist, read *scheme.Prim, path string, maxBytes int64) bool {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxBytes || !utf8.ValidString(path) || pin.Resolve(path) == "" {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	for _, p := range []string{path, real} {
		if deny.Check(ctx, read, []scheme.Value{scheme.Str(p)}).Verdict == guard.Deny {
			return false
		}
	}
	return true
}
