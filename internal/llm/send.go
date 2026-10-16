package llm

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// newClient is the client every provider sends with: it follows no redirect
// (NoRedirect), and drops a connection that sat idle for 90 s.
func newClient() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment, IdleConnTimeout: 90 * time.Second},
		CheckRedirect: NoRedirect,
	}
}

// send is the one place a provider's request is authorized, sent and
// checked, so a fix to a step of it (Authorize before the request leaves, a
// redirect refused before the answer is read, the cap on what a refusal may
// say) is made once. authorize puts the key in, after the binding check, and
// is nil for a server that takes none; a failure of it ends the request
// before anything is sent. A redirect is an error, and a permanent one:
// asking again gets the same answer. A response that is not 200 is the
// provider's refusal (refused). provider names the errors it makes.
func send(client *http.Client, authorize func(*http.Request) error, provider string, req *http.Request) (*http.Response, error) {
	if authorize != nil {
		if err := authorize(req); err != nil {
			return nil, fmt.Errorf("%s: %w", provider, err)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", provider, err)
	}
	if err := Redirected(resp, "model requests"); err != nil {
		resp.Body.Close()
		return nil, Permanent(fmt.Errorf("%s: %w", provider, err))
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, refused(provider, resp, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// ListedModel is one model of a server's list: its id, and how many tokens
// its context holds when the server says (0 when it does not).
type ListedModel struct {
	ID            string
	ContextLength int
}

// contextOf is the context length models gives model, 0 and an error when
// the server does not say.
func contextOf(provider string, models []ListedModel, model string) (int, error) {
	for _, m := range models {
		if m.ID != model {
			continue
		}
		if m.ContextLength > 0 {
			return m.ContextLength, nil
		}
		return 0, fmt.Errorf("%s: the model list does not give %s's context length", provider, model)
	}
	return 0, fmt.Errorf("%s: %s is not in the server's model list", provider, model)
}

// oaiCacheControl is the request's ask to cache the prompt, the same in both
// protocols that take it.
type oaiCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// cacheAsk is the ask a cache_control setting names ("ephemeral" or "1h"):
// nil for "", which asks nothing.
func cacheAsk(setting string) *oaiCacheControl {
	switch setting {
	case "":
		return nil
	case "1h":
		return &oaiCacheControl{Type: "ephemeral", TTL: "1h"}
	}
	return &oaiCacheControl{Type: setting}
}
