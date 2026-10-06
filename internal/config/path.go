package config

import "path/filepath"

// CanonicalPath gives a folder one identity through symlinks, including
// macOS's /tmp and /var aliases. Missing suffixes keep the resolved parent,
// so creating a directory does not change the identity of its settings.
func CanonicalPath(path string) string {
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	rest := ""
	for dir := path; ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		if filepath.Dir(dir) == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}
