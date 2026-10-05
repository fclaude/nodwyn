package llm

import (
	"fmt"
	"regexp"
)

// An effort is how much a reasoning model is asked to think before it
// answers (ADR 41). It is a word the person's config.toml declares for a model
// (`efforts = ["low", "medium", "high"]`) and nodwyn passes on as it is: which
// words a server accepts is the server's to say, so no vocabulary is built
// in, and none is inferred from a URL or a model name. The empty effort sends
// nothing, which leaves the model to its provider's default.
//
// A chat-completions server is sent it as reasoning_effort, the Responses API
// as reasoning.effort, and Anthropic as output_config.effort beside adaptive
// thinking (anthropic.go). Ollama is sent it as think: "on" and "off" are the
// booleans, and any other word is the level a model such as gpt-oss takes
// ("low", "medium", "high").
var effortWord = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,15}$`)

// ValidEffort says whether word may be declared as an effort: lower-case, as
// a provider's own words are, and short, since it is shown on a button.
func ValidEffort(word string) error {
	if !effortWord.MatchString(word) {
		return fmt.Errorf("effort %q must be a short lower-case word such as low, medium or high", word)
	}
	return nil
}

// thinkSetting is the value of Ollama's think field for an effort: the
// booleans for on and off, the word itself for a level, and nothing for no
// effort, which leaves the model to Ollama's own default.
func thinkSetting(effort string) any {
	switch effort {
	case "":
		return nil
	case "on":
		return true
	case "off":
		return false
	}
	return effort
}
