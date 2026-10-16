package config

import (
	"reflect"
	"slices"
	"testing"
)

// A model's settings are its entry's: [control], which names the model as
// the default, and a choice of it by server and id get them from there, and
// what a table sets itself stands. Another model, or the same id at another
// address, gets none of them, and a second entry for the model, here one with
// a smaller context, lends [control] nothing.
func TestSettingsComeFromTheEntryForTheSameModel(t *testing.T) {
	c, err := Parse([]byte(`
[servers.openai]
provider = "openai"
url = "https://api.openai.com/v1"
credential = "openai-key"

[control]
server = "openai"
model = "gpt-5"
max_output = 4096

[models.gpt-5]
server = "openai"
model = "gpt-5"
num_ctx = 400000
max_output = 128000
vision = true
price_input = 1.25
price_cached_input = 0.125
price_output = 10.0

[models.small-gpt-5]
server = "openai"
model = "gpt-5"
num_ctx = 1000
cache_control = "1h"
`))
	if err != nil {
		t.Fatal(err)
	}
	control := c.Tuned(c.Control)
	if control.NumCtx != 400000 || control.Vision == nil || !*control.Vision || control.PriceInput == nil || *control.PriceInput != 1.25 || control.PriceCachedInput == nil || *control.PriceCachedInput != 0.125 || control.PriceOutput == nil || *control.PriceOutput != 10 {
		t.Errorf("[control] did not take its entry's settings: %+v", control)
	}
	if control.CacheControl != "" {
		t.Errorf("[control] took %q from a second entry for the model", control.CacheControl)
	}
	if control.MaxOutput != 4096 {
		t.Errorf("[control]'s own max_output %d was replaced", control.MaxOutput)
	}
	if byID := c.Tuned(c.Resolve(Model{Server: "openai", Model: "gpt-5"})); byID.NumCtx != 400000 || byID.PriceOutput == nil {
		t.Errorf("a choice by server and id did not find the entry's settings: %+v", byID)
	}
	if other := c.Tuned(c.Resolve(Model{Server: "openai", Model: "gpt-5-mini"})); other.NumCtx != 0 || other.Vision != nil || other.PriceInput != nil {
		t.Errorf("another model on the server took them: %+v", other)
	}
	if elsewhere := c.Tuned(Model{Provider: "openai", URL: "https://elsewhere.example/v1", Model: "gpt-5"}); elsewhere.NumCtx != 0 || elsewhere.PriceInput != nil {
		t.Errorf("the same id at another address took them: %+v", elsewhere)
	}
}

// A field added to Model has to say what it is (P2-072): the copies made of a
// model (WithSettings, Choice) read the scope, so one that has none is
// copied nowhere and the default model silently ignores it. Every setting is
// carried by WithSettings, and nothing else is.
func TestEveryModelFieldHasAScope(t *testing.T) {
	typ := reflect.TypeFor[Model]()
	full := reflect.New(typ).Elem()
	for i := range typ.NumField() {
		field := typ.Field(i)
		scope := field.Tag.Get("scope")
		if !slices.Contains([]string{"identity", "choice", "setting", "entry"}, scope) {
			t.Errorf("Model.%s has scope %q: give it identity, choice, setting or entry", field.Name, scope)
			continue
		}
		// A non-zero value of the field's type, whatever it is.
		v := full.Field(i)
		switch v.Kind() {
		case reflect.String:
			v.SetString("x")
		case reflect.Int, reflect.Int64:
			v.SetInt(1)
		case reflect.Bool:
			v.SetBool(true)
		case reflect.Pointer:
			v.Set(reflect.New(v.Type().Elem()))
		case reflect.Slice:
			v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		default:
			t.Fatalf("Model.%s is a %s: teach this test to fill one", field.Name, v.Kind())
		}
	}
	got := reflect.ValueOf(Model{}.WithSettings(full.Interface().(Model)))
	for i := range typ.NumField() {
		carried := !got.Field(i).IsZero()
		if setting := typ.Field(i).Tag.Get("scope") == "setting"; carried != setting {
			t.Errorf("WithSettings carries Model.%s = %v, but its scope is %q", typ.Field(i).Name, carried, typ.Field(i).Tag.Get("scope"))
		}
	}
}

// Choice keeps which model it is and where, and drops its settings.
func TestChoiceDropsTheSettings(t *testing.T) {
	price, vision := 1.0, false
	m := Model{Server: "s", Provider: "openai", URL: "https://h/v1", Credential: "k", Model: "m", Effort: "high", Efforts: []string{"low", "high"}, NumCtx: 9, Vision: &vision, PriceInput: &price}
	got := m.Choice()
	if got.Server != "s" || got.Provider != "openai" || got.URL != "https://h/v1" || got.Credential != "k" || got.Model != "m" || got.Effort != "high" {
		t.Errorf("Choice lost which model it is: %+v", got)
	}
	if got.NumCtx != 0 || got.Vision != nil || got.PriceInput != nil || got.Efforts != nil {
		t.Errorf("Choice kept a setting: %+v", got)
	}
}
