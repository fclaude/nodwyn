package config

import "testing"

// Servers are keyed by name, so two at one address (an API key and a ChatGPT
// plan) resolve to their own credentials, and a model on one is not the same
// model as the id on the other.
func TestModelsAtSameURLResolveByServerName(t *testing.T) {
	c, err := Parse([]byte(`
[servers.openai]
provider = "openai"
url = "https://api.openai.com/v1"
credential = "key"

[servers.plan]
provider = "openai"
dialect = "responses"
url = "https://api.openai.com/v1"
credential = "chatgpt"

[models.a]
server = "openai"
model = "gpt-5"
max_output = 100

[models.b]
server = "plan"
model = "gpt-5"
max_output = 200
`))
	if err != nil {
		t.Fatal(err)
	}
	a, b := c.Resolve(c.Models["a"]), c.Resolve(c.Models["b"])
	if a.Credential != "key" || b.Credential != "chatgpt" {
		t.Fatalf("credentials: %q, %q", a.Credential, b.Credential)
	}
	if c.SameModel(a, b) {
		t.Error("two servers at one address are one model")
	}
	if got := c.Tuned(Model{Server: "plan", Model: "gpt-5"}); got.MaxOutput != 200 {
		t.Errorf("plan's settings: %+v", got)
	}
	if got := c.Tuned(Model{Server: "openai", Model: "gpt-5"}); got.MaxOutput != 100 {
		t.Errorf("key's settings: %+v", got)
	}
}
