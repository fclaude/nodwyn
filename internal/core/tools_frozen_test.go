package core

import (
	"context"
	"strings"
	"testing"
)

// ADR 9 fixes the tool list at the end of construction: a tool a session
// offers cannot register another, whoever runs the tool.
func TestASessionTakesNoToolRegistrationAfterConstruction(t *testing.T) {
	t.Parallel()
	s, err := New(diskOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.interp.EvalString(context.Background(), `(register-tool "late" (lambda () 1) '((doc . "d") (params . ())))`)
	if err == nil || !strings.Contains(err.Error(), "the tool list is fixed for the session") {
		t.Fatalf("a registration after construction: %v", err)
	}
	if _, ok := s.interp.Lookup("late"); ok {
		t.Fatal("the tool was registered")
	}
}
