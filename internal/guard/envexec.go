package guard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// envExecutable reports whether p, a command's program, is a program in the
// bin folder of a Python environment: a conda environment (its folder holds
// conda-meta) or a virtualenv (pyvenv.cfg), whose files only root or the
// person can change. The system prompt tells the model to find the project's
// environment and run its interpreter (prompts.scm), and naming that
// interpreter by its full path is what finding it gives: on the long-context
// benchmark (ADR 17, amended 2026-10-10) 98 of the 223 commands refused in
// nodwyn's attempts did that, and the model got round each refusal with
// conda run or a script that walked /opt, which the guard does not see as a
// path at all. Running it adds nothing a program on PATH does not have: its
// arguments are still read for paths, and the environment is not one anybody
// else could have put there, since a program, its folder and the
// environment's folder that another user owns, or that anyone but their
// owner may write, are not let through.
func envExecutable(p string) bool {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return false
	}
	bin := filepath.Dir(p)
	if filepath.Base(bin) != "bin" {
		return false
	}
	env := filepath.Dir(bin)
	if !isEnvironment(env) {
		return false
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	info, err := os.Stat(real)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return false
	}
	for _, path := range []string{real, filepath.Dir(real), bin, env} {
		if !ownedSafely(path) {
			return false
		}
	}
	return true
}

// isEnvironment reports whether dir is a Python environment's folder: it
// holds conda-meta (a conda environment, or conda's own base) or pyvenv.cfg
// (a virtualenv).
func isEnvironment(dir string) bool {
	conda, cerr := os.Stat(filepath.Join(dir, "conda-meta"))
	venv, verr := os.Stat(filepath.Join(dir, "pyvenv.cfg"))
	return (cerr == nil && conda.IsDir()) || (verr == nil && venv.Mode().IsRegular())
}

// inEnvironment reports whether path, or every match of it when it is a
// glob, lies where it really points in a Python environment's folder that
// only root or the person can change: the installed packages, the
// environment's interpreters and conda's activation scripts. A path with a
// parent component is not judged, since cleaning it before its links are
// resolved could hide where it leads.
func inEnvironment(path string) bool {
	if !filepath.IsAbs(path) || slices.Contains(strings.Split(path, string(filepath.Separator)), "..") {
		return false
	}
	paths := []string{filepath.Clean(path)}
	if strings.ContainsAny(path, "*?[") {
		matches, err := filepath.Glob(path)
		if err != nil || len(matches) == 0 {
			return false
		}
		paths = matches
	}
	for _, p := range paths {
		if environmentOf(resolveExisting(p)) == "" {
			return false
		}
	}
	return true
}

// environmentOf returns the folder of the Python environment p is in, the
// nearest one above it or p itself, when only root or the person can
// change it; "" otherwise.
func environmentOf(p string) string {
	for dir := p; ; dir = filepath.Dir(dir) {
		if isEnvironment(dir) {
			if ownedSafely(dir) {
				return dir
			}
			return ""
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
	}
}

// ownedSafely reports whether path belongs to root or to the person nodwyn
// runs as, and nobody else may write it.
func ownedSafely(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o022 != 0 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && (st.Uid == 0 || int(st.Uid) == os.Getuid())
}
