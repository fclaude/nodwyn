package guard

import (
	"path/filepath"
	"regexp"
	"strings"
)

// A shell's -c line is a command line of its own, and the system prompt
// sends every pipe, redirect and chain through `bash -c` (prompts.scm). A
// standing approval for "bash" was therefore an approval for every command
// (ADR 17, amended 2026-10-05). So a standing approval names the program a
// line runs, never the interpreter that runs it, and it covers a -c line
// only when every command in it is one the person approved, joined by
// &&, || or ;, piped only into filters that read what came before them,
// with no redirection to a file, no substitution, no variable and no
// subshell. Anything else asks as it always did.

// shells are the programs whose -c argument is a command line.
var shells = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true}

// interpreters run whatever their arguments say, so approving one by name
// approves everything: they are never offered as a standing prefix.
var interpreters = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"python": true, "python3": true, "node": true, "deno": true, "bun": true, "perl": true, "ruby": true, "php": true,
	"env": true, "xargs": true, "sudo": true, "doas": true, "nohup": true, "nice": true, "timeout": true, "exec": true,
	"eval": true, "ssh": true, "watch": true, "busybox": true,
}

// shellCommand is one simple command of a -c line, its words unquoted, and
// whether a pipe feeds it.
type shellCommand struct {
	words []string
	piped bool
}

// harmlessRedirects may appear in a line a standing approval covers: they
// send output nowhere but the terminal or the bin.
var harmlessRedirects = map[string]bool{"2>&1": true, ">/dev/null": true, "2>/dev/null": true, "&>/dev/null": true, "1>/dev/null": true}

// shellLine splits a -c line into its simple commands, or reports false when
// the line holds anything a standing approval must not cover: a
// substitution, a variable, a subshell or group, a background job, a
// redirection other than harmlessRedirects, an assignment before a command,
// or an unclosed quote.
func shellLine(line string) ([]shellCommand, bool) {
	if strings.ContainsAny(line, "`$(){}<") {
		return nil, false
	}
	var (
		out   []shellCommand
		words []string
		word  strings.Builder
		in    bool // a word is being read
		piped bool
	)
	endWord := func() {
		if in {
			words = append(words, word.String())
			word.Reset()
			in = false
		}
	}
	endCommand := func(nextPiped bool) bool {
		endWord()
		var kept []string
		for _, w := range words {
			if !harmlessRedirects[w] {
				kept = append(kept, w)
			}
		}
		if len(kept) == 0 {
			return false
		}
		for _, w := range kept {
			if strings.Contains(w, ">") || strings.Contains(w, "&") {
				return false
			}
		}
		out = append(out, shellCommand{words: kept, piped: piped})
		words, piped = nil, nextPiped
		return true
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
			word.WriteString(strings.ReplaceAll(line[i+1:j], `\"`, `"`))
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
		case c == ';' || c == '\n':
			if !endCommand(false) {
				return nil, false
			}
		case c == '|' || c == '&':
			two := i+1 < len(line) && line[i+1] == c
			switch {
			case two: // && or ||
				if !endCommand(false) {
					return nil, false
				}
				i++
			case c == '|':
				if !endCommand(true) {
					return nil, false
				}
			default: // a lone & is a background job, unless it is part of a redirect word
				if in && strings.HasSuffix(word.String(), ">") {
					word.WriteByte(c)
					continue
				}
				if i+1 < len(line) && line[i+1] == '>' && !in {
					word.WriteByte(c)
					in = true
					continue
				}
				return nil, false
			}
		default:
			word.WriteByte(c)
			in = true
		}
	}
	endWord()
	if len(words) > 0 && !endCommand(false) {
		return nil, false
	}
	if len(out) == 0 || piped {
		return nil, false
	}
	for _, cmd := range out {
		if strings.Contains(cmd.words[0], "=") {
			return nil, false
		}
	}
	return out, true
}

// shellCommands is the commands of argv when it is a shell's -c line: the
// commands and true when the line is one a standing approval may cover, nil
// and true when it is a -c line it may not, and false when argv is not a
// -c line at all.
func shellCommands(argv []string) ([]shellCommand, bool) {
	if len(argv) < 3 || !shells[filepath.Base(argv[0])] || argv[1] != "-c" {
		return nil, false
	}
	if len(argv) > 3 {
		return nil, true // positional parameters the line may expand
	}
	cmds, ok := shellLine(argv[2])
	if !ok {
		return nil, true
	}
	for _, c := range cmds {
		if interpreters[filepath.Base(c.words[0])] {
			return nil, true
		}
	}
	return cmds, true
}

var (
	// The flags each filter may take: none names a file to read or write.
	filterFlags = map[string]*regexp.Regexp{
		"head": regexp.MustCompile(`^-([cn]?[0-9]+|[cnqv])$|^--(lines|bytes)=[0-9]+$`),
		"tail": regexp.MustCompile(`^-([cn]?[0-9]+|[cnqv])$|^--(lines|bytes)=[0-9]+$`),
		"wc":   regexp.MustCompile(`^-[clmwL]+$`),
		"sort": regexp.MustCompile(`^-[bdfgiMhnRrsuVz]+$`),
		"uniq": regexp.MustCompile(`^-[cdiuz]+$`),
		"grep": regexp.MustCompile(`^-[viEFncqowxshHABCm]+$|^--(invert-match|ignore-case|extended-regexp|fixed-strings|line-number|count|quiet|only-matching|word-regexp|line-regexp|no-messages|max-count=[0-9]+)$`),
	}
	number = regexp.MustCompile(`^[0-9]+$`)
)

// filter reports whether cmd, fed by a pipe, only shapes what came before
// it: head, tail, wc, sort or uniq with their flags and counts, or grep with
// its flags and at most one pattern. None of them is given a file.
func filter(cmd shellCommand) bool {
	flags, ok := filterFlags[cmd.words[0]]
	if !cmd.piped || !ok {
		return false
	}
	patterns := 0
	for _, a := range cmd.words[1:] {
		switch {
		case flags.MatchString(a), number.MatchString(a):
		case cmd.words[0] == "grep" && a == "-e":
		case cmd.words[0] == "grep" && !strings.HasPrefix(a, "-"):
			patterns++
		default:
			return false
		}
	}
	return patterns <= 1
}
