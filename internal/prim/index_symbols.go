package prim

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/scheme"
	"github.com/fclaude/nibb/internal/symbols"
)

func indexSymbols(o Options, pin guard.RepoPin) *scheme.Prim {
	return &scheme.Prim{
		Name: "index-symbols",
		Meta: scheme.Meta{
			Doc:    "Outline named definitions in one Go, JavaScript or Python file using bundled Tree-sitter grammars. Returns language, definitions (name, kind, line, end_line), parse_errors and truncated. At most 100 definitions. This is syntax retrieval, not type analysis; use index-grep for unsupported languages.",
			Params: []scheme.Param{{Name: "path", Type: "string", Doc: "source file relative to the repository", Role: "path"}},
			Host:   true, TaintSource: scheme.TagFile,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			path, err := resolve(pin, c, 0)
			if err != nil {
				return scheme.Value{}, err
			}
			language, err := symbols.Language(path)
			if err != nil {
				return scheme.Value{}, err
			}
			read, ok := c.In.Lookup("fs-read")
			if !ok {
				return scheme.Value{}, fmt.Errorf("fs-read is unavailable")
			}
			if !searchableFile(c.Ctx, pin, read, path, o.MaxReadBytes) {
				return scheme.Value{}, fmt.Errorf("select a regular, unprotected source file within the read limit; symlinks are excluded")
			}
			value, err := c.In.Apply(c.Ctx, scheme.Value{Kind: scheme.KPrim, Prim: read}, []scheme.Value{scheme.Str(path)})
			if err != nil {
				return scheme.Value{}, err
			}
			if !utf8.ValidString(value.S) || strings.ContainsRune(value.S, 0) {
				return scheme.Value{}, fmt.Errorf("source must be UTF-8 text")
			}
			ctx, cancel := context.WithTimeout(c.Ctx, 5*time.Second)
			defer cancel()
			result, err := symbols.Parse(ctx, language, []byte(value.S))
			if ctx.Err() != nil {
				return scheme.Value{}, ctx.Err()
			}
			if err != nil {
				return scheme.Value{}, err
			}
			raw, err := json.Marshal(result)
			if err != nil {
				return scheme.Value{}, err
			}
			if len(raw) > o.MaxOutput {
				return scheme.Value{}, fmt.Errorf("symbol output exceeds %d bytes", o.MaxOutput)
			}
			return scheme.FromJSON(raw)
		},
	}
}
