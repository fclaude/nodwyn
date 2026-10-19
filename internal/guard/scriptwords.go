package guard

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
)

// The argv trigger asks a person about a command line that names a path
// outside the repository. shellLine splits a -c line only when a standing
// approval may cover it, and the trigger used to read any other -c line as
// one word: a script that began with an absolute path, or held an
// assignment such as PYTHONPATH=/testbed before a continued line, was then
// flagged whole ("names a path outside the repository: /testbed python \"),
// and a path in the middle of a script that did not parse was never looked
// at. On the long-context benchmark (ADR 17, amended 2026-10-10) 48 of the
// 223 refusals in nodwyn's attempts were such scripts, the repository's own
// path among them. scriptWords splits every line it can, so each word is
// judged on its own; a line it cannot split (an unclosed quote) is still
// read as one word.

// scriptCommand is one simple command of a -c line as the argv trigger reads
// it: its words, assignments before the program included, the files its
// redirections name, whether it runs inside a command substitution, whose
// output another command may then use as its own arguments, and whether its
// output goes into a pipe, where the next command may do the same.
type scriptCommand struct {
	words   []string
	targets []string
	sub     bool
	piped   bool
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// keywords open a compound command; the word after one is a program.
var keywords = map[string]bool{"if": true, "then": true, "elif": true, "else": true, "do": true, "while": true, "until": true, "!": true, "{": true}

// program is the index of a command's program among words: the first word
// that is not an assignment or a keyword, or len(words) when there is none.
func program(words []string) int {
	for i, w := range words {
		if !assignment.MatchString(w) && !keywords[w] {
			return i
		}
	}
	return len(words)
}

// scriptWords splits a shell's -c line into its simple commands for the argv
// trigger. Unlike shellLine it reads lines with variables, substitutions,
// subshells, background jobs and redirections, since it only says which
// words a line holds, never that the line is safe: the inside of a $(...)
// or a backquoted command and of a subshell is a command of its own, a
// redirection's target is a file the command names, and a here-document's
// body is data, as it was when the whole line was one word. A backslash
// before a newline joins the lines, as in the shell. It reports false for a
// line it cannot split.
func scriptWords(line string) ([]scriptCommand, bool) {
	line = strings.ReplaceAll(line, "\\\n", "")
	var (
		out     []scriptCommand
		cur     scriptCommand
		word    strings.Builder
		in      bool
		target  bool     // the word being read is a redirection's target
		pending []string // the delimiters of here-documents whose bodies start at the next newline
		nesting []byte   // the open $( (s) and ( (p) around the command being read
		tick    bool     // inside a backquoted command
	)
	endWord := func() {
		if !in {
			return
		}
		if target {
			cur.targets = append(cur.targets, word.String())
			target = false
		} else {
			cur.words = append(cur.words, word.String())
		}
		word.Reset()
		in = false
	}
	endCommand := func() {
		endWord()
		if len(cur.words) > 0 || len(cur.targets) > 0 {
			cur.sub = tick || bytes.IndexByte(nesting, 's') >= 0
			out = append(out, cur)
		}
		cur = scriptCommand{}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\'':
			j := strings.IndexByte(line[i+1:], '\'')
			if j < 0 {
				return nil, false
			}
			word.WriteString(line[i+1 : i+1+j])
			in = true
			i += j + 1
		case c == '"':
			j := i + 1
			for ; j < len(line) && line[j] != '"'; j++ {
				switch {
				case line[j] == '\\' && j+1 < len(line):
					j++
				case line[j] == '$' && j+1 < len(line) && line[j+1] == '(', line[j] == '`':
					// The shell runs a substitution inside double quotes
					// too: "$(ls /etc)" is a command of its own.
					from := j + 1
					if line[j] == '$' {
						from = j + 2
					}
					end := substEnd(line, from, line[j] == '`')
					if end < 0 {
						return nil, false
					}
					inner, ok := scriptWords(line[from:end])
					if !ok {
						return nil, false
					}
					for _, cmd := range inner {
						cmd.sub = true
						out = append(out, cmd)
					}
					j = end
				}
			}
			if j >= len(line) {
				return nil, false
			}
			for k := i + 1; k < j; k++ {
				if line[k] == '\\' && k+1 < j && strings.IndexByte("\"\\$`", line[k+1]) >= 0 {
					k++
				}
				word.WriteByte(line[k])
			}
			in = true
			i = j
		case c == '\\':
			if i+1 >= len(line) {
				return nil, false
			}
			word.WriteByte(line[i+1])
			in = true
			i++
		case c == ' ' || c == '\t':
			endWord()
		case c == '$' && i+1 < len(line) && line[i+1] == '(':
			endCommand()
			nesting = append(nesting, 's')
			i++
		case c == '&' && i+1 < len(line) && line[i+1] == '>':
			// &> and &>> send both streams to the file that follows.
			endWord()
			for i+1 < len(line) && line[i+1] == '>' {
				i++
			}
			target = true
		case c == '<' || c == '>':
			// Digits just before the operator are a file descriptor.
			if in && !target && strings.Trim(word.String(), "0123456789") == "" {
				word.Reset()
				in = false
			}
			endWord()
			if c == '<' && i+1 < len(line) && line[i+1] == '<' {
				if i+2 < len(line) && line[i+2] == '<' { // a here-string: the word after it is text
					i = wordEnd(line, i+3)
					continue
				}
				// A here-document: its delimiter follows, and its body
				// starts at the next newline.
				start := i + 2
				if start < len(line) && line[start] == '-' {
					start++
				}
				end := wordEnd(line, start)
				pending = append(pending, strings.Trim(strings.TrimSpace(line[start:end+1]), `'"\`))
				i = end
				continue
			}
			for i+1 < len(line) && (line[i+1] == '>' || line[i+1] == '<' || line[i+1] == '|') {
				i++
			}
			if i+1 < len(line) && line[i+1] == '&' {
				// >&2 and 2>&1 duplicate a descriptor, >&- closes one; any
				// other word is a file, as after &>: >&/path sends both
				// streams there.
				i++
				fd := false
				for i+1 < len(line) && strings.IndexByte("0123456789-", line[i+1]) >= 0 {
					i++
					fd = true
				}
				if fd {
					continue
				}
			}
			target = true
		case c == '|':
			// A pipe feeds the command before it, or the group a ) closed, to
			// the one after (|& with its error output too); || is an or-list,
			// which feeds nothing.
			if i+1 < len(line) && line[i+1] == '|' {
				i++
				endCommand()
				continue
			}
			if i+1 < len(line) && line[i+1] == '&' {
				i++
			}
			endCommand()
			if n := len(out); n > 0 {
				out[n-1].piped = true
			}
		case c == '\n' || c == ';' || c == '&' || c == '(' || c == ')' || c == '`':
			endCommand()
			switch {
			case c == '(':
				nesting = append(nesting, 'p')
			case c == ')' && len(nesting) > 0:
				nesting = nesting[:len(nesting)-1]
			case c == '`':
				tick = !tick
			}
			if c == '\n' {
				for _, delim := range pending {
					i = bodyEnd(line, i+1, delim)
				}
				pending = nil
			}
		default:
			word.WriteByte(c)
			in = true
		}
	}
	endCommand()
	return out, true
}

// substEnd returns the index that closes a substitution whose inside starts
// at start: the next backquote of a backquoted command (tick), else the
// matching ) of a $(; -1 when nothing closes it. Quotes and escapes inside a
// $( are skipped.
func substEnd(line string, start int, tick bool) int {
	if tick {
		for k := start; k < len(line); k++ {
			switch line[k] {
			case '\\':
				k++
			case '`':
				return k
			}
		}
		return -1
	}
	depth := 1
	for k := start; k < len(line); k++ {
		switch line[k] {
		case '\\':
			k++
		case '\'':
			j := strings.IndexByte(line[k+1:], '\'')
			if j < 0 {
				return -1
			}
			k += j + 1
		case '"':
			for k++; k < len(line) && line[k] != '"'; k++ {
				if line[k] == '\\' {
					k++
				}
			}
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return k
			}
		}
	}
	return -1
}

// wordEnd returns the index of the last character of the word that starts
// at or after start, past any blanks before it: a word ends at a blank, a
// newline or an operator.
func wordEnd(line string, start int) int {
	j := start
	for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
		j++
	}
	for j < len(line) && strings.IndexByte(" \t\n;|&<>()", line[j]) < 0 {
		j++
	}
	return j - 1
}

// bodyEnd returns the index of the newline that ends a here-document's
// closing line, the body starting at start, or the last index of line when
// no line closes it.
func bodyEnd(line string, start int, delim string) int {
	pos := start
	for pos < len(line) {
		next := len(line)
		if end := strings.IndexByte(line[pos:], '\n'); end >= 0 {
			next = pos + end
		}
		if strings.TrimSpace(line[pos:next]) == delim {
			return min(next, len(line)-1)
		}
		pos = next + 1
	}
	return len(line) - 1
}

// wrappers run the program named among their arguments: the first word
// after their own flags (and after timeout's duration, env's assignments)
// is a program too.
var wrappers = map[string]bool{"env": true, "nice": true, "nohup": true, "time": true, "timeout": true, "exec": true, "command": true}

// programs returns the indexes of a command's program and of the programs
// its wrappers run.
func programs(words []string) map[int]bool {
	at := map[int]bool{}
	i := program(words)
	for i < len(words) {
		at[i] = true
		base := filepath.Base(words[i])
		if !wrappers[base] {
			break
		}
		i++
		for i < len(words) && (strings.HasPrefix(words[i], "-") || (base == "env" && assignment.MatchString(words[i]))) {
			i++
		}
		if base == "timeout" && i < len(words) {
			i++ // the duration
		}
	}
	return at
}

// patternWords marks the words of a command that are a pattern or a script,
// not a file: sed's and awk's script, grep's, rg's and git grep's pattern,
// the values of their -e, and git log's -L range. A sed address such as
// /^def polyval/,/^def /p looks like an absolute path and is none; a
// file they read (-f, an operand after the pattern) stays a file.
func patternWords(words []string) map[int]bool {
	marked := map[int]bool{}
	p := program(words)
	if p >= len(words) {
		return marked
	}
	base := filepath.Base(words[p])
	args := words[p+1:]
	at := func(j int) int { return p + 1 + j }
	switch base {
	case "git":
		for j := 0; j < len(args); j++ {
			if args[j] == "-L" && j+1 < len(args) {
				marked[at(j+1)] = true
			}
		}
		for j, a := range args {
			if a == "grep" {
				for k, v := range firstOperand(args[j+1:], "-e", "--regexp", "-f", "--file") {
					marked[at(j+1+k)] = v
				}
				break
			}
		}
	case "sed", "gsed":
		for k, v := range firstOperand(args, "-e", "--expression", "-f", "--file") {
			marked[at(k)] = v
		}
	case "grep", "egrep", "fgrep", "rg", "ag":
		for k, v := range firstOperand(args, "-e", "--regexp", "-f", "--file") {
			marked[at(k)] = v
		}
	case "awk", "gawk", "mawk":
		for k, v := range firstOperand(args, "", "", "-f", "--file") {
			marked[at(k)] = v
		}
	}
	return marked
}

// firstOperand marks, in args, the values of the expression flag (short
// and long), or, when neither it nor the file flag is given, the first word
// that is not a flag: the expression a pattern tool takes before its files.
func firstOperand(args []string, short, long, fileShort, fileLong string) map[int]bool {
	marked := map[int]bool{}
	given := false
	for j := 0; j < len(args); j++ {
		a := args[j]
		switch {
		case short != "" && (a == short || a == long) && j+1 < len(args):
			marked[j+1] = true
			given = true
			j++
		case short != "" && strings.HasPrefix(a, long+"="):
			given = true
		case a == fileShort || a == fileLong || strings.HasPrefix(a, fileLong+"="):
			given = true
		}
	}
	if given {
		return marked
	}
	for j, a := range args {
		if a == "--" {
			if j+1 < len(args) {
				marked[j+1] = true
			}
			return marked
		}
		if !strings.HasPrefix(a, "-") {
			marked[j] = true
			return marked
		}
	}
	return marked
}

// devices are files every program may name: no data of the person's lives
// there.
var devices = map[string]bool{"/dev/null": true, "/dev/stdin": true, "/dev/stdout": true, "/dev/stderr": true, "/dev/tty": true, "/dev/zero": true, "/dev/random": true, "/dev/urandom": true}

// How a command treats the paths among its words, by its program. The
// trigger asks a person about a path outside the repository because a
// command may read what is there, change it or run it; what a command only
// lists, only prints or only reads in a Python environment needs no
// question. Reading the line word by word took the trigger, on the 2,874
// shell commands of the long-context benchmark, from 226 refusals to 442,
// for commands of these kinds: finding the environment (ls /opt/*/envs,
// find / -name python3.6), activating it (source
// /opt/miniconda3/etc/profile.d/conda.sh, 89 of them), reading its packages,
// and for loops over candidate interpreters (ADR 17, amended 2026-10-10:
// what a command does with a path). What these only name or print is not read
// when nothing takes it for arguments, and something does when it is in a
// substitution, goes into a pipe or reaches a command that reads its input
// for them (ADR 17, amended 2026-10-19).
const (
	accessAny    = iota // it may read, change or run what it is given
	accessNames         // names and metadata only
	accessReads         // contents, read and never changed
	accessSource        // the file runs in the shell itself
	accessPrints        // no file access: what echo prints, unless it is taken as arguments
	accessLoop          // a for loop's list: the body takes each word as a path or a program
)

// namesOnly show the names and metadata of what they are given; find is one
// unless it runs, deletes or writes (writingFlags).
var namesOnly = map[string]bool{"ls": true, "find": true, "stat": true, "which": true, "whereis": true, "type": true, "readlink": true, "realpath": true, "dirname": true, "basename": true, "du": true, "test": true, "[": true, "pwd": true}

// readers read what they are given and write nothing; sort and rg only
// without the flags in writingFlags. sed and awk are not among them: their
// scripts can write files and run commands.
var readers = map[string]bool{"cat": true, "head": true, "tail": true, "less": true, "more": true, "grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "wc": true, "diff": true, "cmp": true, "file": true, "strings": true, "od": true, "hexdump": true, "md5sum": true, "sha1sum": true, "sha256sum": true, "nl": true, "tac": true, "sort": true}

// writingFlags make a names-only or reading program one that runs, deletes
// or writes: find's actions, sort's output file, rg's preprocessor.
var writingFlags = map[string][]string{
	"find": {"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls"},
	"sort": {"-o", "--output"},
	"rg":   {"--pre"},
}

// access is how the command of words treats its paths: by the program a
// wrapper runs, if any, else by its program.
func access(words []string) int {
	at := -1
	for i := range programs(words) {
		at = max(at, i)
	}
	if at < 0 {
		return accessAny
	}
	base := filepath.Base(words[at])
	for _, a := range words[at+1:] {
		for _, f := range writingFlags[base] {
			if a == f || strings.HasPrefix(a, f+"=") || (f == "-o" && strings.HasPrefix(a, "-o")) || (f == "--pre" && strings.HasPrefix(a, f)) {
				return accessAny
			}
		}
	}
	switch {
	case namesOnly[base]:
		return accessNames
	case readers[base]:
		return accessReads
	case base == "source" || base == ".":
		return accessSource
	case base == "for":
		return accessLoop
	case base == "echo" || base == "printf":
		return accessPrints
	}
	return accessAny
}

// inputTakers turn the lines of their input into arguments of a command, or
// into the variables a loop's body then uses as paths: what an echo or a find
// writes before them is a list of the files they act on. A program that opens
// what its input names in its own code (a script reading its standard input)
// is out of sight of a reader of words, which only says what a line spells out.
var inputTakers = map[string]bool{"xargs": true, "parallel": true, "read": true, "mapfile": true, "readarray": true}

// takesInput reports whether the command of words, or a program a wrapper runs
// in it, is one of inputTakers.
func takesInput(words []string) bool {
	for i := range programs(words) {
		if inputTakers[filepath.Base(words[i])] {
			return true
		}
	}
	return false
}

// ShellCommands is the simple commands a command line runs, each as its
// words from the program on, joined by single spaces: the inside of a shell's
// -c line read as scriptWords reads it, so a quoted pattern, a here-document's
// body and a redirection's target are no command, and the commands inside a
// substitution are left out. argv that is no -c line is one command. The
// evidence loop matches the commands a report cites against these (ADR 32,
// amended 2026-10-16): read by its plain words, every line of a heredoc and
// every word after a | inside a grep pattern had counted as a program run.
// A line scriptWords cannot split gives nil.
func ShellCommands(argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	if len(argv) < 3 || !shells[filepath.Base(argv[0])] || argv[1] != "-c" {
		return []string{strings.Join(argv, " ")}
	}
	cmds, ok := scriptWords(argv[2])
	if !ok {
		return nil
	}
	var out []string
	for _, c := range cmds {
		if c.sub {
			continue
		}
		if p := program(c.words); p < len(c.words) {
			out = append(out, strings.Join(c.words[p:], " "))
		}
	}
	return out
}
