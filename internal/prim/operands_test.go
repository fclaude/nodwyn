package prim

import (
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/scheme"
)

const setupOld = `package rpc

func (s *server) settings() result {
	judge := s.cfg.Jev.Enabled() && s.keys != nil && s.keys.Has(jevClass)
	stored := s.keys != nil && s.keys.Has(jevClass)
	if !s.allowed(s.host) {
		return result{}
	}
	if err != nil {
		return result{}
	}
	return result{Judge: judge, Stored: stored}
}

func (s *server) other() bool {
	return s.a() || s.b()
}
`

func droppedNames(ds []droppedOperand) []string {
	var out []string
	for _, d := range ds {
		g := ""
		if d.guard {
			g = " (guard)"
		}
		out = append(out, d.function+": "+d.operand+g)
	}
	return out
}

// The operands of an old body's && and || that the new body no longer
// holds are named, and so is a guard's condition: dsl19 dropped the judge's
// Enabled() on c80b38ac, and no test had a case where it was false (ADR 32,
// amended 2026-10-15). The keychain operands it also dropped from that line
// still stand in the stored line, so they are not named; neither is an error
// check, nor a function only reformatted.
func TestDroppedOperandsAreThePartsTheNewBodyLacks(t *testing.T) {
	now := strings.Replace(setupOld, "judge := s.cfg.Jev.Enabled() && s.keys != nil && s.keys.Has(jevClass)", "judge := state == ready", 1)
	now = strings.Replace(now, "\tif !s.allowed(s.host) {\n\t\treturn result{}\n\t}\n", "", 1)
	now = strings.Replace(now, "\tif err != nil {\n\t\treturn result{}\n\t}\n", "", 1)
	now = strings.Replace(now, "return s.a() || s.b()", "return s.a() ||\n\t\ts.b()", 1)
	c := fakeChange(map[string]string{"rpc/setup.go": setupOld}, map[string]string{"rpc/setup.go": now})
	got := droppedNames(droppedOperands(c, "The judge's key is ready only when it is stored."))
	want := []string{"server.settings: s.cfg.Jev.Enabled()", "server.settings: !s.allowed(s.host) (guard)"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("dropped %q, want %q", got, want)
	}
}

// A condition moved into a helper the new body calls, one the change added
// or rewrote, is still checked; one the request names (the request is about
// it) is the task's to change; a test file's conditions are not read.
func TestDroppedOperandsLeaveMovedAndNamedConditionsAlone(t *testing.T) {
	moved := strings.Replace(setupOld, "judge := s.cfg.Jev.Enabled() && s.keys != nil && s.keys.Has(jevClass)", "judge := s.judgeReady()", 1)
	moved = strings.Replace(moved, "\tif !s.allowed(s.host) {\n\t\treturn result{}\n\t}\n", "", 1)
	head := map[string]string{"rpc/setup.go": setupOld, "rpc/setup_test.go": "package rpc\n\nfunc f() bool { return a && b }\n"}
	now := map[string]string{
		"rpc/setup.go":      moved,
		"rpc/judge.go":      "package rpc\n\nfunc (s *server) judgeReady() bool {\n\tif !s.allowed(s.host) {\n\t\treturn false\n\t}\n\treturn s.cfg.Jev.Enabled() && s.keys != nil\n}\n",
		"rpc/setup_test.go": "package rpc\n\nfunc f() bool { return a }\n",
	}
	if got := droppedOperands(fakeChange(head, now), ""); len(got) != 0 {
		t.Errorf("a condition moved into a new helper: dropped %q", droppedNames(got))
	}
	gone := strings.Replace(setupOld, "judge := s.cfg.Jev.Enabled() && s.keys != nil && s.keys.Has(jevClass)", "judge := state == ready", 1)
	c := fakeChange(map[string]string{"rpc/setup.go": setupOld}, map[string]string{"rpc/setup.go": gone})
	if got := droppedOperands(c, "The judge no longer needs jev to be enabled."); len(got) != 0 {
		t.Errorf("a condition the request names: dropped %q", droppedNames(got))
	}
}

// (change-dropped-conditions request) reads the working tree against HEAD.
func TestChangeDroppedConditionsReadsTheWorkingTree(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "rpc/setup.go", setupOld, 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "rpc")
	write(t, root, "rpc/setup.go", strings.Replace(setupOld, "s.cfg.Jev.Enabled() && ", "", 1), 0o644)
	in := scheme.New()
	if err := in.Register(droppedOperandsPrim(k.o)); err != nil {
		t.Fatal(err)
	}
	v, err := in.EvalString(t.Context(), `(change-dropped-conditions "make the judge need its key")`)
	if err != nil {
		t.Fatal(err)
	}
	got := v.String()
	for _, want := range []string{`("path" . "rpc/setup.go")`, `("function" . "server.settings")`, `("operand" . "s.cfg.Jev.Enabled()")`, `("guard" . #f)`} {
		if !strings.Contains(got, want) {
			t.Errorf("change-dropped-conditions lacks %s: %s", want, got)
		}
	}
}
