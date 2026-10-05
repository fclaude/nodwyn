package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// StatusError is a provider's refusal of a request: the status it answered
// and what it said. Its text is what a request that failed has always said,
// "openai: 401 Unauthorized: {…}", so nothing that reads it changes; a caller
// that must tell a rejected key from a missing model asks for the status.
type StatusError struct {
	Provider string
	Status   int
	// Text is the status line, such as "401 Unauthorized".
	Text string
	Body string
	// RetryAfter is how long the server asked to be left alone (its
	// Retry-After header), 0 when it did not say.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string { return fmt.Sprintf("%s: %s: %s", e.Provider, e.Text, e.Body) }

// refused is the error of a request the provider answered with status. A
// request refused for what it was — a key the server does not accept, a model
// it does not have, a field it does not know — is refused again when it is
// sent again, so those are Permanent: a turn that has a configuration to fix
// does not wait through the retries (six seconds) to say so.
//
// So is 402: an account out of credit is out of it a few seconds later too,
// and its two retries only delayed saying so (session
// 20261004T182412Z-747045aa, seq 6842 to 6847).
func refused(provider string, resp *http.Response, body string) error {
	err := &StatusError{Provider: provider, Status: resp.StatusCode, Text: resp.Status, Body: body, RetryAfter: retryAfter(resp.Header, time.Now())}
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity:
		return Permanent(err)
	}
	return err
}

// retryAfter reads how long a server asked to wait: Retry-After in seconds
// or as a date, or the milliseconds of retry-after-ms, which OpenAI and
// servers like it send; 0 when it says neither.
func retryAfter(h http.Header, now time.Time) time.Duration {
	if ms, err := strconv.ParseFloat(strings.TrimSpace(h.Get("Retry-After-Ms")), 64); err == nil && ms > 0 {
		return time.Duration(ms * float64(time.Millisecond))
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil {
		if s <= 0 {
			return 0
		}
		return time.Duration(s * float64(time.Second))
	}
	if at, err := http.ParseTime(v); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

// Hint says what to do about a failed request when its status shows a
// configuration problem a person can fix, or "" when it does not. It is added
// to the text of the failure a turn ends with, so a wrong key, a model the
// provider does not have and an effort it refuses read as such and not as a
// bare status.
func Hint(err error) string {
	var se *StatusError
	if !errors.As(err, &se) {
		return ""
	}
	switch se.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "The server did not accept the key: replace it in Settings › Models, or choose a model on another provider"
	case http.StatusNotFound:
		return "The server has no such model: check its id in Settings › Models, or choose another model"
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		// OpenAI refuses an effort beside tools on /chat/completions for some
		// models and names the API that takes them (session
		// 20261005T033050Z-a7ed2b66): another effort would only hide that.
		if strings.Contains(se.Body, "/v1/responses") {
			return "The server takes this request only on its Responses API: set the provider's protocol to OpenAI Responses in Settings › Models"
		}
		// What Anthropic refuses for a model older than the request assumes:
		// an effort asks for adaptive thinking (Claude 4.6 and later), and the
		// newest models take no temperature.
		if se.Provider == "anthropic" {
			body := strings.ToLower(se.Body)
			switch {
			case strings.Contains(body, "adaptive"):
				return "The model does not take adaptive thinking, which an effort asks for (Claude 4.6 and later do): choose the model's default effort, or remove its efforts in Settings › Models"
			case strings.Contains(body, "temperature"):
				return "The model takes no temperature: clear it for this model in Settings › Models"
			}
		}
		// A chat-completions model is sent images unless its entry says it
		// takes none (ADR 22, amended 2026-10-05), so the first image is how a
		// model that cannot read one is found.
		if strings.Contains(strings.ToLower(se.Body), "image") {
			return "The server refused an image: this model may not take images. Choose one that does from the picker under the message box, and set Takes images to No for this one in Settings › Models"
		}
		return "The server refused the request. If the effort is the cause, choose another effort, or the model's default, from the picker under the message box"
	case http.StatusPaymentRequired:
		return "The provider wants payment: the account is out of credit. Add credit on its billing page, or choose a model on another provider"
	case http.StatusTooManyRequests:
		return "The provider is limiting requests or the account is out of quota: wait, or choose another model"
	}
	return ""
}
