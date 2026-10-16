package must

import (
	"os"
	"path/filepath"
	"testing"
)

// failure records what a helper told the test, so a failing setup can be
// seen to fail it without failing this test.
type failure struct {
	testing.TB
	failed bool
}

func (f *failure) Helper() {}
func (f *failure) Fatalf(string, ...any) {
	f.failed = true
	panic(f) // as FailNow stops the test goroutine
}

func fails(fn func(t testing.TB)) (failed bool) {
	f := &failure{}
	defer func() {
		if r := recover(); r != nil && r != f {
			panic(r)
		}
		failed = f.failed
	}()
	fn(f)
	return false
}

func TestSetupThatWorksPassesAndSetupThatFailsFailsTheTest(t *testing.T) {
	dir := t.TempDir()
	if fails(func(t testing.TB) {
		MkdirAll(t, filepath.Join(dir, "a", "b"), 0o755)
		WriteFile(t, filepath.Join(dir, "a", "b", "f"), []byte("x"), 0o600)
		Symlink(t, filepath.Join(dir, "a", "b", "f"), filepath.Join(dir, "link"))
	}) {
		t.Fatal("working setup failed the test")
	}
	if got, err := os.ReadFile(filepath.Join(dir, "link")); err != nil || string(got) != "x" {
		t.Fatalf("the setup did not happen: %q %v", got, err)
	}
	// Each helper fails the test when its call fails: the folder is a file,
	// and the link's name is taken.
	for name, fn := range map[string]func(t testing.TB){
		"WriteFile": func(t testing.TB) { WriteFile(t, filepath.Join(dir, "a", "b", "f", "g"), nil, 0o600) },
		"MkdirAll":  func(t testing.TB) { MkdirAll(t, filepath.Join(dir, "a", "b", "f", "g"), 0o755) },
		"Symlink":   func(t testing.TB) { Symlink(t, "x", filepath.Join(dir, "link")) },
	} {
		if !fails(fn) {
			t.Errorf("%s: a failed setup did not fail the test", name)
		}
	}
}
