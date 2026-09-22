package core

import (
	"testing"

	"github.com/fclaude/nibb/internal/binding"
)

func TestBindingInspectionCannotMutateAuthorization(t *testing.T) {
	o := diskOptions(t)
	o.Bindings = binding.Table{Bind: []binding.Binding{{Class: "service", Dest: "api.example.test", Positions: []string{"header:Authorization"}, Methods: []string{"POST"}}}}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows := s.Bindings()
	rows[0].Dest = "evil.test"
	rows[0].Methods[0] = "DELETE"
	rows[0].Positions[0] = "body"
	current := s.Bindings()
	if current[0].Dest != "api.example.test" || current[0].Methods[0] != "POST" || current[0].Positions[0] != "header:Authorization" {
		t.Fatal("inspector mutated binding table")
	}
	if ok, _ := s.bindings.Lookup("service", "api.example.test", "POST", "header:Authorization"); !ok {
		t.Fatal("authorization changed")
	}
}
