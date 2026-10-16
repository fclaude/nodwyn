package llm

import "fmt"

// ChatDialect names a server's interpretation of Chat Completions, or, for
// ChatResponses, the Responses API OpenAI defined after it (responses.go). It
// is explicit because a URL may be a proxy and a response field need not be
// accepted in conversation history. The zero value keeps existing requests.
type ChatDialect string

const (
	ChatCompatible ChatDialect = "compatible"
	ChatCerebras   ChatDialect = "cerebras"
	ChatResponses  ChatDialect = "responses"
)

// Validate refuses unknown dialects instead of silently changing the wire
// format when a configuration misspells the server's name.
func (d ChatDialect) Validate() error {
	switch d {
	case "", ChatCompatible, ChatCerebras, ChatResponses:
		return nil
	default:
		return fmt.Errorf("dialect %q is not supported (compatible, cerebras, responses)", d)
	}
}

// KeepsDetails says whether a reply's reasoning_details are kept and given
// back on this dialect: the compatible one's, which is OpenRouter's. Cerebras
// refuses history fields it does not know, and the Responses API keeps its
// reasoning another way (OpenAI.ReasoningOrigin).
func (d ChatDialect) KeepsDetails() bool { return d == "" || d == ChatCompatible }

// setThinking selects the history field independently of response decoding:
// Cerebras returns reasoning but refuses reasoning_content on the next turn.
func (d ChatDialect) setThinking(m *oaiMessage, thinking string) {
	if d == ChatCerebras {
		m.Reasoning = thinking
	} else {
		m.ReasoningContent = thinking
	}
}
