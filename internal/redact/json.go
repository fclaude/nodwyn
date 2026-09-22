package redact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/fclaude/nibb/internal/detect/l1"
)

// JSON redacts decoded strings, including object keys and nested tool arguments.
// Scanning serialized JSON alone misses escaped credentials and PEM newlines.
func JSON(raw []byte, scanner *l1.Scanner) ([]byte, []Event, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, nil, err
	}
	var events []Event
	clean := func(s string) string {
		r := Redact(s, scanner)
		events = append(events, r.Events...)
		return r.Text
	}
	var walk func(any) (any, error)
	walk = func(v any) (any, error) {
		switch x := v.(type) {
		case string:
			// Tool values may themselves be JSON text. Decode that layer so
			// escaped PEM newlines and unicode cannot hide a credential.
			if len(x) > 0 && (x[0] == '{' || x[0] == '[') && json.Valid([]byte(x)) {
				nested, found, err := JSON([]byte(x), scanner)
				if err != nil {
					return nil, err
				}
				if len(found) > 0 {
					events = append(events, found...)
					return string(nested), nil
				}
			}
			return clean(x), nil
		case []any:
			for i := range x {
				y, err := walk(x[i])
				if err != nil {
					return nil, err
				}
				x[i] = y
			}
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, val := range x {
				key := clean(k)
				if _, exists := out[key]; exists {
					return nil, fmt.Errorf("redact: object keys collide after redaction")
				}
				y, err := walk(val)
				if err != nil {
					return nil, err
				}
				out[key] = y
			}
			return out, nil
		}
		return v, nil
	}
	v, err := walk(v)
	if err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(v)
	return b, events, err
}

// Payload preserves the concrete event type so frontends only receive the
// sanitized representation while keeping their typed event contract.
func Payload(v any, scanner *l1.Scanner) (any, []Event, error) {
	if v == nil {
		return nil, nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	b, events, err := JSON(b, scanner)
	if err != nil {
		return nil, nil, err
	}
	dst := reflect.New(reflect.TypeOf(v))
	if err := json.Unmarshal(b, dst.Interface()); err != nil {
		return nil, nil, err
	}
	return dst.Elem().Interface(), events, nil
}
