package llm

import "fmt"

// ChatDialect names a server's interpretation of Chat Completions. It is
// explicit because a URL may be a proxy and a response field need not be
// accepted in conversation history. The zero value keeps existing requests.
type ChatDialect string

const (
	ChatCompatible ChatDialect = "compatible"
	ChatCerebras   ChatDialect = "cerebras"
)

// Validate refuses unknown dialects instead of silently changing the wire
// format when a configuration misspells the server's name.
func (d ChatDialect) Validate() error {
	switch d {
	case "", ChatCompatible, ChatCerebras:
		return nil
	default:
		return fmt.Errorf("dialect %q is not supported (compatible, cerebras)", d)
	}
}

// setThinking selects the history field independently of response decoding:
// Cerebras returns reasoning but refuses reasoning_content on the next turn.
func (d ChatDialect) setThinking(m *oaiMessage, thinking string) {
	if d == ChatCerebras {
		m.Reasoning = thinking
	} else {
		m.ReasoningContent = thinking
	}
}
