package prim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/scheme"
)

const searchFileLimit = 128
const searchMatchLimit = 100
const searchTimeout = 30 * time.Second

type searchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type searchResult struct {
	Matches   []searchMatch `json:"matches"`
	Files     int           `json:"files"`
	Skipped   int           `json:"skipped"`
	Truncated bool          `json:"truncated"`
}

func indexGrep(o Options, pin guard.RepoPin) *scheme.Prim {
	return &scheme.Prim{
		Name: "index-grep",
		Meta: scheme.Meta{
			Doc:    "Search a repository file or directory with a ripgrep regular expression. Returns matches (path, line, text), files searched, files skipped and a truncated flag. At most 128 candidate files and 100 matching lines; narrow the path for larger searches. Protected files, symlinks, binary and oversized files are skipped. Requires rg on PATH.",
			Params: []scheme.Param{{Name: "path", Type: "string", Doc: "repository file or directory", Role: "path"}, {Name: "pattern", Type: "string", Doc: "ripgrep regular expression, not command-line options"}},
			Host:   true, TaintSource: scheme.TagFile,
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
			rg, err := exec.LookPath("rg")
			if err != nil {
				return scheme.Value{}, fmt.Errorf("install ripgrep (rg) on PATH to use index-grep: %w", err)
			}
			ctx, cancel := context.WithTimeout(c.Ctx, searchTimeout)
			defer cancel()
			// Validate even when discovery returns no searchable files.
			if _, err := runRipgrep(ctx, rg, o.Root, "", o.MaxOutput, "--json", "--regexp", pattern, "--", "-"); err != nil {
				return scheme.Value{}, err
			}
			files, err := searchFiles(ctx, rg, o.Root, path)
			if err != nil {
				return scheme.Value{}, err
			}
			read, ok := c.In.Lookup("fs-read")
			if !ok {
				return scheme.Value{}, fmt.Errorf("fs-read is unavailable")
			}
			result := searchResult{Matches: []searchMatch{}}
			outputBytes := 0
			for _, file := range files {
				if err := ctx.Err(); err != nil {
					return scheme.Value{}, err
				}
				if !searchableFile(ctx, pin, read, file, o.MaxReadBytes) {
					result.Skipped++
					continue
				}
				// Nested reads take the same single guarded application path as direct
				// model calls, including user-defined checkers. Denials are never swallowed.
				value, err := c.In.Apply(ctx, scheme.Value{Kind: scheme.KPrim, Prim: read}, []scheme.Value{scheme.Str(file)})
				if err != nil {
					return scheme.Value{}, err
				}
				if !utf8.ValidString(value.S) || strings.ContainsRune(value.S, 0) {
					result.Skipped++
					continue
				}
				raw, err := runRipgrep(ctx, rg, o.Root, value.S, o.MaxOutput, "--json", "--max-count=101", "--regexp", pattern, "--", "-")
				if err != nil {
					return scheme.Value{}, err
				}
				result.Files++
				relative, err := filepath.Rel(o.Root, file)
				if err != nil {
					return scheme.Value{}, err
				}
				decoder := json.NewDecoder(bytes.NewReader(raw))
				for {
					var message struct {
						Type string
						Data struct {
							LineNumber int `json:"line_number"`
							Lines      struct{ Text string }
						}
					}
					if err := decoder.Decode(&message); err == io.EOF {
						break
					} else if err != nil {
						return scheme.Value{}, fmt.Errorf("ripgrep output: %w", err)
					}
					if message.Type != "match" {
						continue
					}
					text := strings.TrimSuffix(message.Data.Lines.Text, "\n")
					outputBytes += len(relative) + len(text) + 32
					if len(result.Matches) == searchMatchLimit || outputBytes > o.MaxOutput {
						result.Truncated = true
						break
					}
					result.Matches = append(result.Matches, searchMatch{filepath.ToSlash(relative), message.Data.LineNumber, text})
				}
				if result.Truncated {
					break
				}
			}
			raw, err := json.Marshal(result)
			if err != nil {
				return scheme.Value{}, err
			}
			return scheme.FromJSON(raw)
		},
	}
}

// Discovery reads names only. Ignore/configuration files are not loaded: even
// a repository ignore file could be a symlink to protected content.
func searchFiles(ctx context.Context, rg, root, path string) ([]string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []string{path}, nil
	}
	raw, err := runRipgrep(ctx, rg, root, "", 1<<20, "--files", "--null", "--sort=path", "--no-follow", "--no-ignore", "--", path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, name := range bytes.Split(raw, []byte{0}) {
		if len(name) > 0 {
			files = append(files, string(name))
		}
	}
	if len(files) > searchFileLimit {
		return nil, fmt.Errorf("search found %d candidate files (limit %d); narrow the path", len(files), searchFileLimit)
	}
	return files, nil
}

// Apply the built-in exclusions before reading content. The subsequent fs-read
// still evaluates the complete guard, so this is filtering, not authorization.
func searchableFile(ctx context.Context, pin guard.RepoPin, read *scheme.Prim, path string, maxBytes int64) bool {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxBytes || !utf8.ValidString(path) || pin.Resolve(path) == "" {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	deny := guard.DefaultDenylist()
	for _, p := range []string{path, real} {
		if deny.Check(ctx, read, []scheme.Value{scheme.Str(p)}).Verdict == guard.Deny {
			return false
		}
	}
	return true
}

// Only fixed ripgrep operations are accepted here. Neither a shell nor user
// configuration runs, and the child inherits no credential-bearing environment.
func runRipgrep(ctx context.Context, rg, root, input string, limit int, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, rg, append([]string{"--no-config"}, args...)...)
	cmd.Dir = root
	cmd.Env = []string{"LC_ALL=C"}
	cmd.Stdin = strings.NewReader(input)
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	stdout := searchOutput{limit: limit}
	cmd.Stdout = &stdout
	cmd.Stderr = &capped{&stderr, 4096}
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if stdout.overflow {
		return nil, fmt.Errorf("ripgrep output exceeds %d bytes; narrow the search", limit)
	}
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return nil, fmt.Errorf("ripgrep: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.buffer.Bytes(), nil
}

// Unlike shell output, structured search output must be complete or rejected.
type searchOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *searchOutput) Write(p []byte) (int, error) {
	n := len(p)
	room := b.limit - b.buffer.Len()
	if len(p) > room {
		b.overflow = true
		p = p[:room]
	}
	b.buffer.Write(p)
	return n, nil
}
