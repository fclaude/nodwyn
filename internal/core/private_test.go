package core

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
)

// The directory a service manager keeps the daemon's credentials in, where
// the keychain's passphrase may be, is closed to a task's path arguments as
// nodwyn's own directories are (ADR 5, amended 2026-10-17): any process of
// the same account can read it otherwise. A relative value is no directory.
func TestTheCredentialsDirectoryIsPrivateToTasks(t *testing.T) {
	dir := t.TempDir()
	paths := config.PathsUnder(filepath.Join(dir, "config"), filepath.Join(dir, "data"))
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	if got := privateDirs(paths); !slices.Equal(got, []string{paths.ConfigDir, paths.DataDir}) {
		t.Fatalf("without credentials: %v", got)
	}
	creds := filepath.Join(dir, "credentials", "nodwyn.service")
	t.Setenv("CREDENTIALS_DIRECTORY", creds)
	if got := privateDirs(paths); !slices.Contains(got, creds) {
		t.Fatalf("with credentials: %v", got)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "relative")
	if got := privateDirs(paths); len(got) != 2 {
		t.Fatalf("a relative directory: %v", got)
	}
}
