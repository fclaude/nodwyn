package guard

import (
	"os"
	"path/filepath"
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
	conda, cerr := os.Stat(filepath.Join(env, "conda-meta"))
	venv, verr := os.Stat(filepath.Join(env, "pyvenv.cfg"))
	if !(cerr == nil && conda.IsDir()) && !(verr == nil && venv.Mode().IsRegular()) {
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
