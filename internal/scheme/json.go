package scheme

import (
	"bytes"
	"encoding/json"
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
		s.Required = append(s.Required, pa.Name)
	}
	return s
}

// ArgsFromJSON orders a JSON object of named arguments into p's positional
// parameters. Unknown or missing names are errors so the model gets a
// precise correction.
func (p *Prim) ArgsFromJSON(raw []byte) ([]Value, error) {
	var named map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 {
		named = map[string]json.RawMessage{}
	} else if err := json.Unmarshal(raw, &named); err != nil {
		return nil, errorf(p.Name, "arguments must be a JSON object: %v", err)
	}
	args := make([]Value, len(p.Meta.Params))
	for i, pa := range p.Meta.Params {
		r, ok := named[pa.Name]
		if !ok {
			return nil, errorf(p.Name, "missing argument %q", pa.Name)
		}
		v, err := FromJSON(r)
		if err != nil {
			return nil, errorf(p.Name, "argument %q: %v", pa.Name, err)
		}
		if err := checkParamType(pa, v); err != nil {
			return nil, errorf(p.Name, "argument %q: %v", pa.Name, err)
		}
		args[i] = v
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
