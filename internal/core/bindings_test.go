package core

import (
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/guard"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
)

func TestBindingInspectionCannotMutateAuthorization(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	o.Paths.Bindings = "" // explicitly in-memory test policy
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

func TestBindingEligibilityRequiresVisibleMatchingScope(t *testing.T) {
	t.Parallel()
	s := &Session{opts: Options{Paths: config.PathsUnder(t.TempDir(), t.TempDir())}}
	scope := guard.EgressScope{Class: "service", Host: "api.test", Method: "POST", Position: "body"}
	row, err := binding.Exact(scope.Class, scope.Host, scope.Method, scope.Position)
	if err != nil {
		t.Fatal(err)
	}
	p := guard.Prompt{Egress: &scope, Binding: &row}
	if !s.bindingEligible(p) {
		t.Fatal("valid proposal ineligible")
	}
	p.Security = true
	if s.bindingEligible(p) {
		t.Fatal("security prompt eligible")
	}
	p.Security = false
	row.Dest = "different.test"
	if s.bindingEligible(p) {
		t.Fatal("scope mismatch eligible")
	}
	row.Dest = scope.Host
	row.Class, scope.Class = "sk-ant-abcdefgh1234", "sk-ant-abcdefgh1234"
	if s.bindingEligible(p) {
		t.Fatal("redacted proposal eligible")
	}
}
