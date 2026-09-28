package scheme

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// FromJSON converts JSON text to a value with the json-parse mapping
// (docs/scheme.md §5): objects become alists with string keys in document
// order, arrays lists, null the symbol null.
func FromJSON(raw []byte) (Value, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := jsonDecode(dec)
	if err != nil {
		return Value{}, err
	}
	return v, nil
}

// ToJSON renders a value with the json-emit mapping.
func ToJSON(v Value) ([]byte, error) {
	var buf bytes.Buffer
	if err := jsonEncode(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ToolSchema is the JSON-schema view of a primitive's parameters, for the
// model's tool list (ADR 4: the schema and the guard read the same Meta).
type ToolSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]toolSchemaProp `json:"properties"`
	Required   []string                  `json:"required"`
}

type toolSchemaProp struct {
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

// Schema builds the JSON schema for p's parameters. Variadic extras are not
// representable and are omitted; tools meant for the model should declare a
// list parameter instead.
func (p *Prim) Schema() ToolSchema {
	s := ToolSchema{Type: "object", Properties: map[string]toolSchemaProp{}, Required: []string{}}
	for _, pa := range p.Meta.Params {
		prop := toolSchemaProp{Description: pa.Doc}
		switch pa.Type {
		case "string", "integer", "number", "boolean":
			prop.Type = pa.Type
		case "list", "vector":
			prop.Type = "array"
		case "alist":
			prop.Type = "object"
		}
		s.Properties[pa.Name] = prop
		if !pa.Optional {
			s.Required = append(s.Required, pa.Name)
		}
	}
	return s
}

// ArgsFromJSON orders a JSON object of named arguments into p's positional
// parameters. Unknown or missing names are errors so the model gets a
// precise correction. Optional parameters that are missing or null at the
// end are left off; one skipped before a later one that is given gets its
// type's empty value ("" for string, 0 for integer and number, #f for
// boolean, () for list and alist, #f otherwise), since models pass options
// by name, not position.
func (p *Prim) ArgsFromJSON(raw []byte) ([]Value, error) {
	var named map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 {
		named = map[string]json.RawMessage{}
	} else if err := json.Unmarshal(raw, &named); err != nil {
		return nil, errorf(p.Name, "arguments must be a JSON object: %v", err)
	}
	args := make([]Value, 0, len(p.Meta.Params))
	var skipped []Param // optional parameters not given, since the last one given
	for _, pa := range p.Meta.Params {
		r, ok := named[pa.Name]
		if ok && pa.Optional && bytes.Equal(bytes.TrimSpace(r), []byte("null")) {
			ok = false
			delete(named, pa.Name)
		}
		if !ok {
			if !pa.Optional {
				return nil, errorf(p.Name, "missing argument %q", pa.Name)
			}
			skipped = append(skipped, pa)
			continue
		}
		for _, s := range skipped {
			args = append(args, emptyValue(s.Type))
		}
		skipped = nil
		v, err := FromJSON(r)
		if err != nil {
			return nil, errorf(p.Name, "argument %q: %v", pa.Name, err)
		}
		if err := checkParamType(pa, v); err != nil {
			return nil, errorf(p.Name, "argument %q: %v", pa.Name, err)
		}
		args = append(args, v)
		delete(named, pa.Name)
	}
	if len(named) > 0 {
		keys := make([]string, 0, len(named))
		for k := range named {
			keys = append(keys, k)
		}
		return nil, errorf(p.Name, "unknown argument(s): %s", strings.Join(keys, ", "))
	}
	return args, nil
}

// emptyValue stands in for an optional argument skipped before one given.
func emptyValue(typ string) Value {
	switch typ {
	case "string":
		return Str("")
	case "integer", "number":
		return Int(0)
	case "list", "alist", "vector":
		return Nil
	default:
		return False
	}
}

func checkParamType(pa Param, v Value) error {
	ok := true
	switch pa.Type {
	case "string":
		ok = v.Kind == KString
	case "integer":
		ok = v.Kind == KInt
	case "number":
		ok = v.IsNumber()
	case "boolean":
		ok = v.Kind == KBool
	case "list", "alist":
		ok = v.IsList()
	case "vector":
		ok = v.Kind == KVec
	}
	if !ok {
		return errorf("type", "expected %s, got %s", pa.Type, Write(v))
	}
	return nil
}

func (in *Interp) registerJSON() {
	in.mustRegister("json-parse", "Parse JSON text into a value (objects become alists with string keys).", []Param{{Name: "s", Type: "string"}}, false, func(c *Call) (Value, error) {
		s, err := c.String(0)
		if err != nil {
			return Value{}, err
		}
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		v, err := jsonDecode(dec)
		if err != nil {
			return Value{}, errorf(c.Name, "%v", err)
		}
		if _, err := dec.Token(); err != io.EOF {
			return Value{}, errorf(c.Name, "trailing data after JSON value")
		}
		return v, nil
	})
	in.mustRegister("json-emit", "Render a value as compact JSON text.", []Param{{Name: "v", Type: "any"}}, false, func(c *Call) (Value, error) {
		var buf bytes.Buffer
		if err := jsonEncode(&buf, c.Args[0]); err != nil {
			return Value{}, errorf(c.Name, "%v", err)
		}
		return Str(buf.String()), nil
	})
}

func jsonDecode(dec *json.Decoder) (Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return Value{}, err
	}
	return jsonFromToken(dec, tok)
}

func jsonFromToken(dec *json.Decoder, tok json.Token) (Value, error) {
	switch t := tok.(type) {
	case nil:
		return Sym("null"), nil
	case bool:
		return Bool(t), nil
	case string:
		return Str(t), nil
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return Int(i), nil
		}
		f, err := t.Float64()
		if err != nil {
			return Value{}, err
		}
		return Float(f), nil
	case json.Delim:
		switch t {
		case '[':
			var items []Value
			for dec.More() {
				v, err := jsonDecode(dec)
				if err != nil {
					return Value{}, err
				}
				items = append(items, v)
			}
			if _, err := dec.Token(); err != nil { // ]
				return Value{}, err
			}
			return List(items...), nil
		case '{':
			var items []Value
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return Value{}, err
				}
				key, ok := kt.(string)
				if !ok {
					return Value{}, fmt.Errorf("object key is not a string")
				}
				v, err := jsonDecode(dec)
				if err != nil {
					return Value{}, err
				}
				items = append(items, Cons(Str(key), v))
			}
			if _, err := dec.Token(); err != nil { // }
				return Value{}, err
			}
			return List(items...), nil
		}
	}
	return Value{}, fmt.Errorf("unexpected token %v", tok)
}

func jsonEncode(buf *bytes.Buffer, v Value) error {
	switch v.Kind {
	case KNil:
		buf.WriteString("[]")
	case KBool:
		if v.I != 0 {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case KInt:
		buf.WriteString(strconv.FormatInt(v.I, 10))
	case KFloat:
		if math.IsNaN(v.F) || math.IsInf(v.F, 0) {
			return fmt.Errorf("cannot emit %s", Write(v))
		}
		buf.WriteString(strconv.FormatFloat(v.F, 'g', -1, 64))
	case KString:
		b, _ := json.Marshal(v.S)
		buf.Write(b)
	case KSymbol:
		if v.S != "null" {
			return fmt.Errorf("cannot emit symbol %s", v.S)
		}
		buf.WriteString("null")
	case KPair:
		items, ok := v.Slice()
		if !ok {
			return fmt.Errorf("cannot emit improper list %s", Write(v))
		}
		if isAlist(items) {
			buf.WriteByte('{')
			for i, it := range items {
				if i > 0 {
					buf.WriteByte(',')
				}
				b, _ := json.Marshal(it.P.Car.S)
				buf.Write(b)
				buf.WriteByte(':')
				if err := jsonEncode(buf, it.P.Cdr); err != nil {
					return err
				}
			}
			buf.WriteByte('}')
			return nil
		}
		buf.WriteByte('[')
		for i, it := range items {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := jsonEncode(buf, it); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	default:
		return fmt.Errorf("cannot emit %s", Write(v))
	}
	return nil
}

func isAlist(items []Value) bool {
	if len(items) == 0 {
		return false
	}
	for _, it := range items {
		if it.Kind != KPair || it.P.Car.Kind != KString {
			return false
		}
	}
	return true
}
