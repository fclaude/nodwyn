package llm

import (
	"errors"
	"fmt"
	"net/http"
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
}

func (e *StatusError) Error() string { return fmt.Sprintf("%s: %s: %s", e.Provider, e.Text, e.Body) }

// refused is the error of a request the provider answered with status. A
// request refused for what it was — a key the server does not accept, a model
// it does not have, a field it does not know — is refused again when it is
// sent again, so those are Permanent: a turn that has a configuration to fix
// does not wait through the retries (six seconds) to say so.
func refused(provider string, resp *http.Response, body string) error {
	err := &StatusError{Provider: provider, Status: resp.StatusCode, Text: resp.Status, Body: body}
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity:
		return Permanent(err)
	}
	return err
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
		return "The server refused the request. If the effort is the cause, choose another effort, or the model's default, from the picker under the message box"
	case http.StatusTooManyRequests:
		return "The provider is limiting requests or the account is out of quota: wait, or choose another model"
	}
	return ""
}
