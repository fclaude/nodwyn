package llm

import (
	"fmt"
	"net/http"
)

// NoRedirect is the CheckRedirect of every client that sends a key or a
// window: the client returns the redirect instead of following it, and
// Redirected turns it into an error. A request was checked for the host it
// was sent to (a key's binding names that host; a model's url is where the
// user sends the window). Following a redirect, Go's client drops only
// Authorization and Cookie, and only for another domain: a key in any other
// header goes along, and a 307 or 308 sends the body again, a keyed
// request's payload or a model's whole window.
func NoRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Redirected is the error for a redirect NoRedirect returned, nil for any
// other response. It names the host the server pointed at and no more: the
// whole address may repeat the request's query, and a key in it. requests
// says which ones nodwyn does not redirect ("keyed requests").
func Redirected(resp *http.Response, requests string) error {
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return nil
	}
	loc, err := resp.Location()
	if err != nil {
		return nil // no Location: nothing Go would have followed
	}
	return fmt.Errorf("the server redirected to %s; nodwyn does not follow redirects for %s", loc.Host, requests)
}
