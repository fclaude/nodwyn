package guard

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// pathArg is one path argument: Role "path", what the primitive acts on
// (and writes, when it is irreversible), or Role "source", a file it only
// reads, whatever else it does (a document it converts or sends).
type pathArg struct {
	raw    string
	source bool
}

// pathArgs yields every argument with Role "path" or "source".
func pathArgs(p *scheme.Prim, args []scheme.Value) []pathArg {
	var out []pathArg
	for i, pa := range p.Meta.Params {
		if (pa.Role == "path" || pa.Role == "source") && i < len(args) && args[i].Kind == scheme.KString {
			out = append(out, pathArg{raw: args[i].S, source: pa.Role == "source"})
		}
	}
	return out
}

// CommandArgs yields the command line of a primitive with Role "argv" parameters
// (a command string and a list of arguments).
func CommandArgs(p *scheme.Prim, args []scheme.Value) []string {
	var out []string
	for i, pa := range p.Meta.Params {
		if pa.Role != "argv" || i >= len(args) {
			continue
		}
		switch args[i].Kind {
		case scheme.KString:
			out = append(out, args[i].S)
		case scheme.KPair, scheme.KNil:
			items, _ := args[i].Slice()
			for _, it := range items {
				if it.Kind == scheme.KString {
					out = append(out, it.S)
				}
			}
		}
	}
	return out
}

// MetaPolicy applies the primitive's own declaration: irreversible actions
// need confirmation. Egress is decided by the E3 checker (guard.Egress),
// which owns the binding table — not here.
type MetaPolicy struct{}

func (MetaPolicy) Name() string { return "meta" }

func (MetaPolicy) Check(_ context.Context, p *scheme.Prim, _ []scheme.Value) Decision {
	if p.Meta.Irreversible {
		if p.Meta.ConfirmReason != "" {
			return Decision{Verdict: Confirm, Reason: p.Meta.ConfirmReason}
		}
		return Decision{Verdict: Confirm, Reason: p.Name + " is irreversible"}
	}
	return Decision{Verdict: Allow}
}

// RepoPin denies path arguments that resolve outside the repository root
// or outside the session's scratch directory when there is one. Symlinks are resolved so a link out of either does not escape the
// pin. Relative paths are always relative to the repository.
type RepoPin struct {
	Root    string // absolute, cleaned
	Scratch string // the session's scratch directory; "" for none
	// Temp is the system's /tmp, resolved, which a lean session nobody
	// approves may use as it uses the scratch directory (TempDir); "" for
	// none, the default.
	Temp string
	// Mounts are the workspace's folders outside the root (ADR 22 §1),
	// reached by absolute path. A read-only mount refuses every write.
	Mounts []Mount
}

// Mount is a folder outside the root a workspace may use.
type Mount struct {
	Path  string // absolute, cleaned
	Write bool
}

func (RepoPin) Name() string { return "repo-pin" }

func (r RepoPin) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	for _, a := range pathArgs(p, args) {
		raw := a.raw
		if !r.inside(raw) {
			where := "the repository " + r.Root
			if r.Scratch != "" {
				where += " and the scratch directory " + r.Scratch
			}
			if len(r.Mounts) > 0 {
				where += " and the workspace's mounted folders"
			}
			// Models write their throwaway scripts to /tmp by habit: on the
			// long-context benchmark 68 writes were refused so in 56 of
			// nodwyn's 107 attempts, though the prompts name $TMPDIR (ADR
			// 17, amended 2026-10-10). The refusal says where the file goes,
			// as the prompts write it.
			if r.Temp != "" {
				// The lean profile's model read the denial as a sandbox's wall
				// and checked $TMPDIR on its next call 15 times of 19, whatever
				// it said: say what was refused, and that nothing was done.
				return Decision{Verdict: Deny, Reason: "path " + raw + " is outside the repository, $TMPDIR and /tmp, so the call was refused and nothing was done"}
			}
			if r.Scratch != "" && filepath.IsAbs(raw) {
				where += "; a throwaway file goes in the scratch directory, written as $TMPDIR/" + filepath.Base(raw) + ", which is " + filepath.Join(r.Scratch, filepath.Base(raw))
			}
			return Decision{Verdict: Deny, Reason: "path " + raw + " is outside " + where}
		}
		// A source is only read, so a read-only mount may supply it even to
		// an irreversible primitive (a document exported elsewhere).
		if m := r.mountOf(raw); m != nil && !m.Write && p.Meta.Irreversible && !a.source {
			return Decision{Verdict: Deny, Reason: "path " + raw + " is in " + m.Path + ", which this workspace mounts read-only"}
		}
	}
	return Decision{Verdict: Allow}
}

// mountOf returns the mount raw resolves into — the innermost one, when
// mounts nest — or nil for the root, the scratch directory and anything else.
func (r RepoPin) mountOf(raw string) *Mount {
	if !filepath.IsAbs(raw) {
		return nil
	}
	resolved := resolveExisting(filepath.Clean(raw))
	if within(resolved, resolveExisting(r.Root)) {
		return nil
	}
	var best *Mount
	bestLen := -1
	for i := range r.Mounts {
		dir := resolveExisting(r.Mounts[i].Path)
		if within(resolved, dir) && len(dir) > bestLen {
			best, bestLen = &r.Mounts[i], len(dir)
		}
	}
	return best
}

// MountRel returns raw relative to the mount it is in, for rules judged
// within a folder (protected paths), and whether it is in one.
func (r RepoPin) MountRel(raw string) (string, bool) {
	m := r.mountOf(raw)
	if m == nil {
		return "", false
	}
	rel, err := filepath.Rel(resolveExisting(m.Path), resolveExisting(filepath.Clean(raw)))
	return rel, err == nil
}

// Resolve returns the absolute path for raw under the root, or "" if it
// escapes. Exported for the primitives, which must use the same rule.
func (r RepoPin) Resolve(raw string) string {
	abs := raw
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(r.Root, abs)
	}
	abs = filepath.Clean(abs)
	// Resolve symlinks on the longest existing prefix so a new file under a
	// linked directory is judged by where the link points.
	resolved := resolveExisting(abs)
	if within(resolved, resolveExisting(r.Root)) {
		return abs
	}
	if r.Scratch != "" && filepath.IsAbs(raw) && within(resolved, resolveExisting(r.Scratch)) {
		return abs
	}
	if filepath.IsAbs(raw) && InTemp(r.Temp, r.Scratch, raw) {
		return abs
	}
	if filepath.IsAbs(raw) && r.mountOf(raw) != nil {
		return abs
	}
	return ""
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

func (r RepoPin) inside(raw string) bool { return r.Resolve(raw) != "" }

func resolveExisting(p string) string {
	cur := p
	var rest []string
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				real = filepath.Join(real, rest[i])
			}
			return real
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// PathDenylist denies path arguments that look like credential material,
// whatever their location. It is defence in depth that stays alongside L1.
type PathDenylist struct {
	Globs []string // matched against the base name, except of code (codeExtensions)
	Dirs  []string // absolute directories that are entirely off limits
	Root  string   // what relative paths are relative to (the repository)
	// Except are directories inside Dirs that are not off limits: the
	// session's scratch directory, which is in the system's temporary
	// directory and so outside Dirs, unless TMPDIR is set inside one of
	// them (nodwyn's data directory, say): then it is the one place there
	// the tools may use. Globs still apply in them.
	Except []string
}

// DefaultDenylist is the bootstrap list.
func DefaultDenylist() PathDenylist {
	home, _ := os.UserHomeDir()
	d := PathDenylist{Globs: []string{".env", ".env.*", "*.pem", "*.key", "id_*", "*.kdbx", "*.p12", "*.pfx", "*.jks", "credentials", "credentials.*", ".netrc", ".npmrc", ".pypirc"}}
	if home != "" {
		for _, sub := range []string{".ssh", ".aws", ".gnupg", ".config/nodwyn", ".local/share/nodwyn", ".config/nibb", ".local/share/nibb", ".config/gcloud", ".azure", ".kube"} {
			d.Dirs = append(d.Dirs, filepath.Join(home, sub))
		}
	}
	return d
}

func (PathDenylist) Name() string { return "path-denylist" }

func (d PathDenylist) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	for _, a := range pathArgs(p, args) {
		if reason := d.Denies(a.raw); reason != "" {
			return Decision{Verdict: Deny, Reason: reason}
		}
	}
	return Decision{Verdict: Allow}
}

// codeExtensions mark a file as source or documentation. A name glob says
// what a credential file is called, and code is called that too:
// credentials.go, credentials.js and id_map.py are code about credentials,
// not credentials, and a task about credentials has to read and change
// them. Refusing them only sent the model to the shell, which reads them
// anyway. A dotfile is not code here (.env.js, .eslintrc.js: configuration
// that may hold a key). Their contents still pass ingress redaction (ADR
// 25), and the protected directories still apply to them.
var codeExtensions = map[string]bool{
	".go": true, ".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".tsx": true, ".jsx": true,
	".vue": true, ".svelte": true, ".py": true, ".rb": true, ".php": true, ".java": true, ".kt": true,
	".scala": true, ".rs": true, ".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true,
	".cs": true, ".swift": true, ".m": true, ".dart": true, ".lua": true, ".pl": true, ".ex": true,
	".exs": true, ".erl": true, ".hs": true, ".ml": true, ".clj": true, ".scm": true, ".el": true,
	".css": true, ".scss": true, ".html": true, ".md": true, ".rst": true, ".tex": true,
}

// Denies returns why raw is credential material, or "" when it is not. A
// primitive that reaches files its arguments do not name (the images a
// document refers to) asks it about each one.
func (d PathDenylist) Denies(raw string) string {
	// Relative paths are the repository's, as the pin reads them, and
	// links are followed: a file may not be reached by another name, so a
	// link called notes.png that points at .env is .env.
	abs := raw
	if !filepath.IsAbs(abs) && d.Root != "" {
		abs = filepath.Join(d.Root, abs)
	}
	abs, _ = filepath.Abs(abs)
	real := resolveExisting(abs)
	for _, name := range []string{filepath.Base(raw), filepath.Base(real)} {
		if g := d.credentialName(name); g != "" {
			return "path " + raw + " matches the credential-file denylist (" + g + ")"
		}
	}
	for _, ex := range d.Except {
		if ex != "" && within(abs, ex) && within(real, resolveExisting(ex)) {
			return ""
		}
	}
	for _, dir := range d.Dirs {
		rdir := resolveExisting(dir)
		if within(abs, dir) || within(real, rdir) {
			return "path " + raw + " is under the protected directory " + dir
		}
	}
	return ""
}

// credentialName returns the glob a file name matches, or "". Case is
// ignored, as macOS's file system ignores it (IsProtected): .ENV opens .env.
func (d PathDenylist) credentialName(base string) string {
	base = strings.ToLower(base)
	if !strings.HasPrefix(base, ".") && codeExtensions[filepath.Ext(base)] {
		return ""
	}
	for _, g := range d.Globs {
		if ok, _ := filepath.Match(strings.ToLower(g), base); ok {
			return g
		}
	}
	return ""
}

// ArgvTrigger asks for confirmation when a command line names a network
// tool, a destructive git/rm invocation, or a path outside the repository,
// including a flag's value. Outside paths need a person: a standing approval
// or the judge cannot grant that filesystem authority. Dynamic shell scripts
// that cannot be parsed retain ordinary confirmation, never standing approval.
type ArgvTrigger struct {
	Root     string
	Scratch  string   // the session's scratch directory, which is not "outside"
	Temp     string   // the system's /tmp, resolved, for a lean session nobody approves (TempDir); "" for none
	Network  []string // program names
	Patterns []string // substrings of the joined command line
}

// DefaultArgvTrigger is the bootstrap list.
func DefaultArgvTrigger(root string) ArgvTrigger {
	return ArgvTrigger{
		Root:     root,
		Network:  []string{"curl", "wget", "nc", "ncat", "netcat", "ssh", "scp", "sftp", "rsync", "telnet", "ftp", "socat"},
		Patterns: []string{"git push", "git reset --hard", "git clean -f", "rm -rf", "rm -fr", "sudo ", "chmod 777"},
	}
}

func (ArgvTrigger) Name() string { return "argv-trigger" }

func (a ArgvTrigger) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	argv := CommandArgs(p, args)
	if len(argv) == 0 {
		return Decision{Verdict: Allow}
	}
	line := strings.Join(argv, " ")
	var reasons []string
	security := false
	note := func(reason string) {
		if !slices.Contains(reasons, reason) {
			reasons = append(reasons, reason)
		}
	}
	// A shell's -c line is read word by word (scriptWords), the shell itself
	// a command of its own; a line that cannot be split, or one with
	// positional parameters after it, is read as it always was, as words of
	// one command.
	commands := []scriptCommand{{words: argv}}
	if len(argv) == 3 && shells[filepath.Base(argv[0])] && argv[1] == "-c" {
		if parsed, ok := scriptWords(argv[2]); ok {
			commands = append([]scriptCommand{{words: argv[:2]}}, parsed...)
		}
	}
	// The network tools and the hard-trigger patterns are looked for in the
	// argv as it is and in each command of a -c line as the shell splits it:
	// bash -c "curl ..." is one word to the first, and "git  push" matches
	// no pattern spelled with one space.
	tools := slices.Clone(argv)
	texts := []string{line}
	for _, command := range commands {
		for i := range programs(command.words) {
			tools = append(tools, command.words[i])
		}
		texts = append(texts, strings.Join(command.words, " "))
	}
	for _, tok := range tools {
		base := filepath.Base(tok)
		for _, n := range a.Network {
			if base == n {
				note("command line uses the network tool " + n)
			}
		}
	}
	for _, pat := range a.Patterns {
		for _, text := range texts {
			if strings.Contains(text, pat) {
				note("command line matches the hard-trigger pattern " + strings.TrimSpace(pat))
				break
			}
		}
	}
	// A command that reads its input for arguments (xargs, a while read
	// loop) takes as its list what another command of the line prints or
	// lists, through a pipe, a group or a file.
	fed := slices.ContainsFunc(commands, func(c scriptCommand) bool { return takesInput(c.words) })
	var flagged []string
	// outside asks about tok when it names a path outside the repository
	// and the scratch directory, unless ok, given the path, lets it be. Each
	// of its paths (a flag's value after =) is judged.
	outside := func(tok string, ok func(string) bool) {
		if devices[tok] {
			return
		}
		for _, part := range strings.Split(a.spelled(tok), "=") {
			if path := a.outsidePart(part, ok); path != "" {
				note("command line names a path outside the repository: " + path)
				flagged = append(flagged, path)
				security = true
				return
			}
		}
	}
	for _, command := range commands {
		programs, patterns := programs(command.words), patternWords(command.words)
		first, kind := program(command.words), access(command.words)
		for i, tok := range command.words {
			switch {
			case patterns[i]:
			case programs[i]:
				// A program is judged as one, whatever kind of command it
				// makes: an ls from outside is no names-only ls.
				if !isSystemBinary(tok) && !envExecutable(tok) {
					outside(tok, nil)
				}
			case assignment.MatchString(tok) && (i < first || declares[filepath.Base(command.words[first])]):
				// An assignment only names its paths, a list of them for
				// PATH and PYTHONPATH: each is judged, and one in a Python
				// environment may be named. export and its kin assign too.
				_, value, _ := strings.Cut(tok, "=")
				for _, part := range strings.Split(value, ":") {
					outside(part, inEnvironment)
				}
			case kind == accessPrints && !command.sub && !command.piped && !fed:
				// What echo prints is text. Taken as arguments (in a
				// substitution, in a pipe, or by a command that reads its
				// input for them) it is a path: cat $(echo /etc/hosts),
				// echo /etc/hosts | xargs cat.
			case kind == accessNames && !command.sub && !fed:
				// Names and metadata are shown, never contents. Inside a
				// substitution, or before a command that reads them for
				// arguments (find /etc | xargs cat), they may become
				// another command's arguments, so there only an
				// environment's may be named.
			case kind == accessNames || kind == accessReads || kind == accessPrints:
				outside(tok, inEnvironment)
			case kind == accessLoop:
				// The body takes each word of the list as a path or a
				// program (for f in /etc/*; do cat $f; done): the
				// interpreters a loop tries are the ones let through.
				outside(tok, func(path string) bool { return isSystemBinary(path) || inEnvironment(path) })
			case kind == accessSource:
				outside(tok, sourceable)
			default:
				outside(tok, nil)
			}
		}
		for _, tok := range command.targets {
			outside(tok, nil)
		}
	}
	// Where nobody could approve a path outside (a lean session nobody
	// approves, which may use /tmp), it is refused outright, and the refusal
	// says what was refused and that nothing ran: a confirmation nobody
	// answers read to the model as a sandbox's wall.
	if a.Temp != "" && len(flagged) > 0 {
		return Decision{Verdict: Deny, Reason: "the command line names " + strings.Join(flagged, ", ") + ", outside the repository, $TMPDIR and /tmp, so it was refused and nothing of it ran", Detail: line}
	}
	// Models write throwaway files to /tmp by habit: the refusal says where
	// they go, as the prompts write it.
	for _, p := range flagged {
		if a.Scratch != "" && (p == "/tmp" || strings.HasPrefix(p, "/tmp/")) {
			reasons = append(reasons, "a throwaway file goes in the scratch directory, written as $TMPDIR/"+filepath.Base(p)+", which is "+filepath.Join(a.Scratch, filepath.Base(p)))
			break
		}
	}
	if len(reasons) > 0 {
		return Decision{Verdict: Confirm, Security: security, Reason: strings.Join(reasons, "; "), Detail: line}
	}
	return Decision{Verdict: Allow}
}

// outsidePart is part, one path of a command-line word, when it reaches
// outside the repository, the scratch directory and /tmp where that is
// allowed (and what ok lets be), else "". A glob pattern reaches its
// leading directory and what it matches (globPlaces), and each is judged as
// that path written out would be, links resolved. Read as written, a
// pattern's components were judged as names: /tmp/nodwyn-scratch-*/x as
// another session's scratch directory, and /tmp/l*/hosts by /tmp alone when
// /tmp/link points at /etc (ADR 32, amended 2026-10-17).
func (a ArgvTrigger) outsidePart(part string, ok func(string) bool) string {
	inside := func(path string) bool {
		return argumentPath(path, a.Root, false) == "" || a.inScratch(path) || InTemp(a.Temp, a.Scratch, path) || (ok != nil && ok(path))
	}
	places, pattern := globPlaces(part, a.Root)
	if !pattern {
		if inside(part) {
			return ""
		}
		return part
	}
	// What ok allows of a pattern it judges whole (a Python environment's,
	// every match in it) stays allowed.
	if ok != nil && ok(part) {
		return ""
	}
	for _, p := range places {
		if !inside(p) {
			return part
		}
	}
	// A pattern in /tmp named as a credential file is (*.pem, id_*) is
	// outside, as such a name is there (InTemp).
	if lead := places[0]; argumentPath(lead, a.Root, false) != "" && !a.inScratch(lead) && DefaultDenylist().credentialName(filepath.Base(part)) != "" {
		return part
	}
	return ""
}

// globPlaces is where a command-line word with a glob pattern (* ? or [ in
// a component) can reach: its leading directory, the path up to the first
// component with one, cleaned, and each path the pattern matches now, a
// relative word's in the repository root. False for a word without a
// pattern, one that starts with - (an option), one Go cannot read as a
// pattern (the shell then passes it as it is), or one with a parent
// component, which is judged as written, as every path with one is: cleaning
// src/../* to the root would hide a link that src is.
func globPlaces(word, root string) ([]string, bool) {
	if !strings.ContainsAny(word, "*?[") || strings.HasPrefix(word, "-") {
		return nil, false
	}
	parts := strings.Split(word, string(filepath.Separator))
	if slices.Contains(parts, "..") {
		return nil, false
	}
	first := slices.IndexFunc(parts, func(p string) bool { return strings.ContainsAny(p, "*?[") })
	lead := strings.Join(parts[:first], string(filepath.Separator))
	switch {
	case lead == "" && filepath.IsAbs(word):
		lead = string(filepath.Separator)
	case lead == "":
		lead = "."
	}
	pattern := word
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(root, pattern)
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, false
	}
	return append([]string{filepath.Clean(lead)}, matches...), true
}

// declares are the shell builtins whose NAME=value arguments are
// assignments, as one before a command is.
var declares = map[string]bool{"export": true, "declare": true, "typeset": true, "local": true, "readonly": true}

// inScratch reports whether path, absolute and without a parent component,
// lies in the session's scratch directory, written with a trailing slash or
// not: "<scratch>/" was asked about on the benchmark.
// spelled is tok with each word that starts at the home or the temporary
// directory written as the shell would expand it: ~, ~/…, $HOME and ${HOME}
// as the home directory, ~name as another's (outside, whoever it is), and
// $TMPDIR and ${TMPDIR} as the scratch directory, which shell-exec gives as
// TMPDIR. Read as written they were words relative to the repository, so
// cat ~/.aws/credentials never asked a person. Other variables stay as
// written.
func (a ArgvTrigger) spelled(tok string) string {
	home, _ := os.UserHomeDir()
	parts := strings.Split(tok, "=")
	for i, word := range parts {
		for _, v := range []struct{ name, dir string }{{"$HOME", home}, {"${HOME}", home}, {"$TMPDIR", a.Scratch}, {"${TMPDIR}", a.Scratch}} {
			if v.dir != "" && (word == v.name || strings.HasPrefix(word, v.name+"/")) {
				word = v.dir + strings.TrimPrefix(word, v.name)
			}
		}
		switch {
		case (word == "~" || strings.HasPrefix(word, "~/")) && home != "":
			word = home + strings.TrimPrefix(word, "~")
		case strings.HasPrefix(word, "~"):
			word = "/" + word
		}
		parts[i] = word
	}
	return strings.Join(parts, "=")
}

// TempDir is the system's /tmp with its links resolved (/private/tmp on
// macOS), or "" when there is none: the directory a lean session nobody
// approves may write to and read from as it does its scratch directory. Its
// model wrote throwaway scripts to /tmp in 10 to 22% of sessions, each refusal
// cost 2.2 calls, and the files never reach the repository's diff (ADR 32,
// amended 2026-10-17).
func TempDir() string {
	real, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		return ""
	}
	if st, err := os.Stat(real); err != nil || !st.IsDir() {
		return ""
	}
	return real
}

// InTemp reports whether path, absolute and without a parent component, lies
// in temp (TempDir) once its links are resolved, and is neither another
// session's scratch directory nor a file named as a credential is (the
// denylist's names, DefaultDenylist). scratch is the session's own scratch
// directory, which may be in temp. "" for temp allows nothing.
func InTemp(temp, scratch, path string) bool {
	if temp == "" || !filepath.IsAbs(path) || slices.Contains(strings.Split(path, string(filepath.Separator)), "..") {
		return false
	}
	resolved := resolveExisting(filepath.Clean(path))
	if !within(resolved, temp) {
		return false
	}
	if scratch != "" && within(resolved, resolveExisting(scratch)) {
		return true
	}
	rel, err := filepath.Rel(temp, resolved)
	if err != nil {
		return false
	}
	if first := strings.Split(rel, string(filepath.Separator))[0]; strings.HasPrefix(first, "nodwyn-scratch-") {
		return false
	}
	return DefaultDenylist().credentialName(filepath.Base(resolved)) == "" && DefaultDenylist().credentialName(filepath.Base(path)) == ""
}

func (a ArgvTrigger) inScratch(path string) bool {
	return a.Scratch != "" && filepath.IsAbs(path) && !slices.Contains(strings.Split(path, string(filepath.Separator)), "..") &&
		within(resolveExisting(filepath.Clean(path)), resolveExisting(a.Scratch))
}

// sourceable reports whether path is a script a shell may source without
// the question: one in a Python environment, as conda's activation scripts
// are, that it and its folder only root or the person can change.
func sourceable(path string) bool {
	real := resolveExisting(filepath.Clean(path))
	return inEnvironment(path) && ownedSafely(real) && ownedSafely(filepath.Dir(real))
}

// isSystemBinary allows absolute paths to executables in the usual bin
// directories without a prompt.
func isSystemBinary(p string) bool {
	for _, prefix := range []string{"/usr/bin/", "/bin/", "/usr/local/bin/", "/opt/homebrew/bin/", "/usr/sbin/", "/sbin/"} {
		if strings.HasPrefix(p, prefix) && !strings.Contains(strings.TrimPrefix(p, prefix), "/") {
			return true
		}
	}
	return false
}
