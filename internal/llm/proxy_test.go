package llm

import (
	"net/http"
	"reflect"
	"testing"
)

// Model traffic must reach the control plane through a proxy when the
// environment names one: a sandbox that only allows egress through an
// allowlist proxy (a benchmark harness) otherwise has no route to the model.
func TestModelClientsUseEnvironmentProxy(t *testing.T) {
	want := reflect.ValueOf(http.ProxyFromEnvironment).Pointer()
	for name, client := range map[string]*http.Client{
		"ollama": NewOllama("http://model:11434").Client,
		"openai": NewOpenAI("http://model:8080", nil).Client,
	} {
		tr, ok := client.Transport.(*http.Transport)
		if !ok || tr.Proxy == nil || reflect.ValueOf(tr.Proxy).Pointer() != want {
			t.Errorf("%s client does not use http.ProxyFromEnvironment", name)
		}
	}
}
