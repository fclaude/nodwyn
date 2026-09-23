// Package symbols derives bounded source outlines from bundled Tree-sitter
// grammars. It owns no files, permissions, session state or persistent cache.
package symbols

import (
	"context"
	"embed"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/tetratelabs/wazero"
	"github.com/zema1/wasitter"
)

//go:embed grammars/*.wasm
var grammars embed.FS

const MaxDefinitions = 100
const maxVisitedNodes = 100000
const maxNameBytes = 512

type Definition struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line"`
}

type Outline struct {
	Language    string       `json:"language"`
	Definitions []Definition `json:"definitions"`
	ParseErrors bool         `json:"parse_errors"`
	Truncated   bool         `json:"truncated"`
}

func Language(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go", nil
	case ".js", ".mjs", ".cjs", ".jsx":
		return "javascript", nil
	case ".py", ".pyi":
		return "python", nil
	default:
		return "", fmt.Errorf("index-symbols supports Go, JavaScript and Python files; use index-grep for other languages")
	}
}

// Parse creates one isolated, memory-limited runtime for this outline. No host
// filesystem, environment or network is supplied to its WASI module. Cancellation
// closes executing guest code, including a parse stuck on malformed input.
func Parse(ctx context.Context, language string, source []byte) (Outline, error) {
	out := Outline{Language: language, Definitions: []Definition{}}
	if len(source) > 1<<20 || !utf8.Valid(source) {
		return out, fmt.Errorf("symbol source must be UTF-8 and at most 1 MiB")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	var wasm []byte
	var err error
	switch language {
	case "javascript":
		wasm = wasitter.BuiltinJavaScriptWASM()
	case "go", "python":
		wasm, err = grammars.ReadFile("grammars/wasitter-" + language + ".wasm")
	default:
		return out, fmt.Errorf("unsupported symbol language %q", language)
	}
	if err != nil {
		return out, err
	}
	runtime, err := wasitter.NewRuntimeWithOptions(ctx, wasm, wasitter.RuntimeOptions{
		RuntimeConfig: wazero.NewRuntimeConfig().WithMemoryLimitPages(2048).WithCloseOnContextDone(true),
	})
	if err != nil {
		return out, err
	}
	defer runtime.Close()
	lang, err := runtime.LoadLanguage("")
	if err != nil {
		return out, err
	}
	defer lang.Close()
	parser, err := wasitter.NewParserWithRuntime(runtime)
	if err != nil {
		return out, err
	}
	defer parser.Close()
	if err := parser.SetLanguage(lang); err != nil {
		return out, err
	}
	tree, err := parser.ParseContext(ctx, source, nil)
	if err != nil {
		return out, err
	}
	defer tree.Close()
	root := tree.RootNode()
	out.ParseErrors = root.HasError()
	// An explicit stack bounds traversal without consuming the Go call stack on
	// deeply nested source. Definitions stay in source order.
	stack := []wasitter.Node{root}
	for visited := 0; len(stack) > 0; visited++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if visited == maxVisitedNodes {
			out.Truncated = true
			break
		}
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if kind := definitionKind(language, node.Type()); kind != "" {
			name := node.ChildByFieldName("name")
			if !name.IsNull() {
				text := name.Content(source)
				if text != "" {
					if len(out.Definitions) == MaxDefinitions {
						out.Truncated = true
						break
					}
					if len(text) > maxNameBytes {
						out.Truncated = true
					} else {
						out.Definitions = append(out.Definitions, Definition{Name: text, Kind: kind, Line: int(name.StartPoint().Row) + 1, EndLine: int(node.EndPoint().Row) + 1})
					}
				}
			}
		}
		for i := node.NamedChildCount() - 1; i >= 0; i-- {
			stack = append(stack, node.NamedChild(i))
		}
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// These node kinds deliberately describe declarations, not references or every
// assignment. Supporting another language requires an explicit grammar and map.
func definitionKind(language, kind string) string {
	switch language {
	case "go":
		switch kind {
		case "function_declaration":
			return "function"
		case "method_declaration":
			return "method"
		case "type_spec", "type_alias":
			return "type"
		}
	case "javascript":
		switch kind {
		case "function_declaration", "generator_function_declaration":
			return "function"
		case "class_declaration":
			return "class"
		case "method_definition":
			return "method"
		}
	case "python":
		switch kind {
		case "function_definition":
			return "function"
		case "class_definition":
			return "class"
		}
	}
	return ""
}
