package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fclaude/nibb/internal/llm"
)

func formatTools(catalog []llm.Tool, name string) (string, error) {
	var out strings.Builder
	if name == "" {
		fmt.Fprintf(&out, "%d tools offered to the model\nUse :tools NAME for an input schema.\n\n", len(catalog))
		for _, tool := range catalog {
			fmt.Fprintf(&out, "%s\n  %s\n\n", tool.Name, tool.Description)
		}
	} else {
		for _, tool := range catalog {
			if tool.Name != name {
				continue
			}
			schema, err := json.MarshalIndent(tool.Parameters, "", "  ")
			if err != nil {
				return "", fmt.Errorf("cannot display schema for %s: %w", name, err)
			}
			fmt.Fprintf(&out, "%s\n%s\n\nInput schema\n%s\n\n", tool.Name, tool.Description, schema)
			break
		}
		if out.Len() == 0 {
			return "", fmt.Errorf("no model tool named %q; :tools lists available names", name)
		}
	}
	out.WriteString("Listing a tool grants no authority; calls still pass through the guard.\nThe catalog is fixed when the session opens.")
	return out.String(), nil
}
