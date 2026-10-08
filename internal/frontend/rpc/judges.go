package rpc

import (
	"net/url"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/prim"
)

// statePlan is a judge whose key is a ChatGPT plan's sign-in, which the
// keychain sends only to the plan's chat endpoints, never to the Decisions
// API (ADR 47).
const statePlan = "plan"

// judgeChoice is one judge a task may ask (ADR 47): the server it is on,
// where its questions go, the model they ask, what they cost, and whether
// it can be asked now. Its model is the provider's judge model, which is
// all either typed-question API offers, so a person never types it.
type judgeChoice struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Host     string `json:"host"`
	Model    string `json:"model"`
	// ProviderModel is the provider's judge model (config.JudgeModels),
	// which Model differs from only when config.toml names another.
	ProviderModel string `json:"provider_model"`
	// PriceInput and PriceOutput are what a question costs, USD per million
	// tokens: [judge]'s own on the server it names, else what the provider
	// publishes; absent when nobody says.
	PriceInput  *float64 `json:"price_input,omitempty"`
	PriceOutput *float64 `json:"price_output,omitempty"`
	// State is ready, or needs_key, locked, unbound or plan, with Detail
	// saying what to do. Ready says the key can be sent; whether the
	// service takes it shows on the judge's first answer.
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	// Default marks [judge]'s server, which new tasks start with.
	Default bool `json:"default,omitempty"`
}

// judges are the judges a task may ask: each server config.JudgeOn takes
// whose provider is Jev, or OpenAI at OpenAI's own address, since another
// OpenAI-compatible server has no Decisions API. The server [judge] names
// is listed whatever its address, so what is saved is always shown.
func (s *server) judges() []judgeChoice {
	cfg := s.options.Config
	out := []judgeChoice{}
	for _, name := range cfg.JudgeChoices() {
		spec, _ := cfg.JudgeOn(name)
		host := prim.JudgeHost(prim.JudgeOptions{URL: spec.URL})
		if spec.Provider == "openai" && host != "api.openai.com" && name != cfg.Judge.Server {
			continue
		}
		c := judgeChoice{Name: name, Provider: spec.Provider, Host: host, Model: spec.Model, ProviderModel: config.JudgeModels[spec.Provider], PriceInput: spec.PriceInput, PriceOutput: spec.PriceOutput, Default: name == cfg.Judge.Server}
		c.State, c.Detail = s.judgeKeyCheck(name, spec.Class, host)
		out = append(out, c)
	}
	return out
}

// judgeKeyCheck is what the keychain and the binding table say of the key
// of the judge on server, as keyCheck says of a provider's, and plan for a
// ChatGPT plan's sign-in, which a provider's check calls ready.
func (s *server) judgeKeyCheck(server, class, host string) (state, detail string) {
	if core.PlanSignIn(s.options.Keychain, class) {
		return statePlan, server + " signs in with a ChatGPT plan, which only reaches its chat models: the judge needs an OpenAI API key"
	}
	if u, err := url.Parse("https://" + host); err == nil {
		host = u.Hostname()
	}
	return s.keyCheck(class, host, prim.JudgePosition)
}
