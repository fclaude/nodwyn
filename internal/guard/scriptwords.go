package guard

import (
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
// it: its words, assignments before the program included, and the files its
// redirections name.
type scriptCommand struct {
	words   []string
	targets []string
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// program is the index of a command's program among words: the first word
// that is not an assignment, or len(words) when there is none.
func program(words []string) int {
	for i, w := range words {
		if !assignment.MatchString(w) {
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
				if line[j] == '\\' && j+1 < len(line) {
					j++
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
			if i+1 < len(line) && line[i+1] == '&' { // >&2, 2>&1: a descriptor, not a file
				i++
				for i+1 < len(line) && strings.IndexByte("0123456789-", line[i+1]) >= 0 {
					i++
				}
				continue
			}
			target = true
		case c == '\n' || c == ';' || c == '|' || c == '&' || c == '(' || c == ')' || c == '`':
			endCommand()
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
