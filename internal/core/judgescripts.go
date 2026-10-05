package core

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// A command that runs a script from the session's own scratch directory
// (ADR 28) names only the script: in session 20261005T054223Z-36b27caa the
// judge saw `bash "$TMPDIR/check.sh" tests`, could not tell what it would
// run or whose it was, and sent each run to the person. It is now told that
// the script is the session's, and at jev.content = "full", where file
// contents may leave (ADR 19), shown its text.

// scriptShown is the most of a script's text the judge is shown, and
// scriptsShown the most scripts one command line describes.
const (
	scriptShown  = 4 << 10
	scriptsShown = 3
)

// scratchScript is a file in the session's scratch directory that a command
// line names.
type scratchScript struct {
	name  string // as the command line names it
	lines int
	text  string // its first scriptShown bytes; "" when it is not text
	cut   bool
}

// scratchScripts are the files in scratch, the session's scratch directory,
// that command names, as $TMPDIR/… (the session's TMPDIR is its scratch
// directory) or by their absolute path: each a regular file that resolves
// inside scratch, at most scriptsShown of them. A file the command names but
// that is not there yet, as one it is about to write, is not described.
func scratchScripts(scratch, command string) []scratchScript {
	if scratch == "" {
		return nil
	}
	root, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		return nil
	}
	named := regexp.MustCompile(`(?:\$\{?TMPDIR\}?|` + regexp.QuoteMeta(scratch) + `)/([A-Za-z0-9._+/-]+)`)
	seen := map[string]bool{}
	var out []scratchScript
	for _, m := range named.FindAllStringSubmatch(command, -1) {
		rel := filepath.Clean(m[1])
		if seen[rel] || len(out) == scriptsShown {
			continue
		}
		seen[rel] = true
		real, err := filepath.EvalSymlinks(filepath.Join(scratch, rel))
		if err != nil {
			continue
		}
		if inside, err := filepath.Rel(root, real); err != nil || inside == "." || strings.HasPrefix(inside, "..") {
			continue
		}
		if script, ok := readScript(m[0], real); ok {
			out = append(out, script)
		}
	}
	return out
}

// readScript reads the regular file at path, named name on the command line:
// its line count, and its text when it is text.
func readScript(name, path string) (scratchScript, bool) {
	f, err := os.Open(path)
	if err != nil {
		return scratchScript{}, false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return scratchScript{}, false
	}
	raw, err := io.ReadAll(io.LimitReader(f, 256<<10))
	if err != nil {
		return scratchScript{}, false
	}
	script := scratchScript{name: name, lines: bytes.Count(raw, []byte("\n"))}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		script.lines++
	}
	if utf8.Valid(raw) && !bytes.ContainsRune(raw, 0) {
		script.text = string(raw)
		if len(script.text) > scriptShown {
			script.text, script.cut = shorten(script.text, scriptShown), true
		}
	}
	return script, true
}

// describe is what the judge is told of the script: whose it is, and with
// full its text.
func (sc scratchScript) describe(full bool) string {
	head := fmt.Sprintf("It runs %s: a %d-line file in this session's own scratch directory, where only this session's tools and commands write; it is not part of the repository.\n", sc.name, sc.lines)
	switch {
	case !full:
		return head
	case sc.text == "":
		return head + "It is not text.\n"
	case sc.cut:
		return head + "Its text, cut at " + fmt.Sprint(scriptShown) + " bytes:\n" + sc.text + "\n"
	}
	return head + "Its text:\n" + sc.text + "\n"
}
