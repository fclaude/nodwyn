package guard

import (
	"path/filepath"
	"slices"
	"strings"
)

// Each flag listed here only adjusts selection, diagnostics or presentation.
// Flags that choose another program, input file, output file or working folder
// are deliberately absent. Unknown flags need review instead of inheriting the
// authority of a prefix; the invoked build or test still runs project code.
type commandFlags struct {
	plain string
	value string
}

var standingFlags = map[string]commandFlags{
	"go test":      {"-race -v -n -x -json -short -failfast -cover", "-run -skip -bench -list -count -timeout -parallel -cpu -shuffle -benchtime -p -tags"},
	"go build":     {"-race -v -n -x", "-p -tags"},
	"go vet":       {"-v -n -x", "-p -tags"},
	"git status":   {"-s -b -z --short --branch --porcelain --show-stash", "--porcelain --untracked-files"},
	"git diff":     {"-p -s -w -b --stat --shortstat --numstat --name-only --name-status --summary --check --cached --staged --quiet --exit-code --no-ext-diff --no-textconv --no-color --minimal --patience --histogram --ignore-all-space --ignore-space-change --ignore-space-at-eol --ignore-blank-lines", "-U --unified --color --word-diff --diff-filter"},
	"git log":      {"-p -s --oneline --stat --shortstat --numstat --name-only --name-status --graph --all --branches --tags --remotes --no-merges --merges --first-parent --reverse --abbrev-commit --no-patch --no-decorate --decorate --no-color --no-ext-diff --no-textconv", "-n --max-count --skip --since --until --after --before --author --committer --grep --format --pretty --decorate --color"},
	"git grep":     {"-n -I -P -E -F -i -w -v -l -L -c -h -H -z --line-number --ignore-case --fixed-strings --perl-regexp --extended-regexp --word-regexp --invert-match --count --files-with-matches --files-without-match --all-match --full-name --break --heading --cached", "-e -A -B -C --regexp --after-context --before-context --context --max-count"},
	"git ls-files": {"-z -s -t -v --stage --cached --deleted --modified --error-unmatch --deduplicate --full-name", ""},
	"make":         {"-n -s -k --just-print --dry-run --recon --silent --quiet --keep-going --no-print-directory --print-directory", "-j --jobs"},
	"gofmt":        {"-l -d", ""},
	"staticcheck":  {"-version", "-checks -f -go -tags -tests"},
	"rg":           {"-n -i -v -l -c -F -w -s -S -U --files --line-number --ignore-case --fixed-strings --word-regexp --count --files-with-matches --no-heading --heading --no-messages", "-e -g -t -A -B -C -m --regexp --glob --type --after-context --before-context --context --max-count --max-depth --color"},
}

func listed(flags, flag string) bool {
	return strings.Contains(" "+flags+" ", " "+flag+" ")
}

// argumentPath finds paths even in flag values, including nested values such
// as -ldflags=-extld=/path. It is intentionally conservative about patterns
// that look like paths: a false positive asks a person rather than opening a
// new file boundary. A relative symlink is checked against the real root.
func argumentPath(token, root string, absolute bool) string {
	for _, part := range strings.Split(token, "=") {
		words := []string{part}
		if absolute {
			words = append(words, strings.Fields(part)...)
		}
		for _, word := range words {
			if absolute && (strings.HasPrefix(word, "~") || strings.ContainsAny(word, "$`")) {
				return word
			}
			// Cleaning before resolving a symlink followed by .. can hide
			// an escape. Parent components therefore always need review.
			if slices.Contains(strings.Split(word, string(filepath.Separator)), "..") {
				return word
			}
			path := filepath.Clean(word)
			if filepath.IsAbs(path) {
				if absolute || root == "" || !within(resolveExisting(path), resolveExisting(root)) {
					return word
				}
				continue
			}
			if root != "" && !strings.HasPrefix(word, "-") && !within(resolveExisting(filepath.Join(root, path)), resolveExisting(root)) {
				return word
			}
		}
	}
	return ""
}

// pathRisk is why a standing approval cannot cover path. A path inside the
// repository, written by the absolute path the system prompt gives for it,
// is refused like any other, and says what to write: a command already runs
// in the repository root (commit-bench, 7513f7eb: a "cd <repository> && go
// test" the model had no need to write, refused for good in an unattended
// run).
func pathRisk(path, root string) string {
	reason := "standing approval cannot cover an absolute or repository-escaping path: " + path
	if root != "" && filepath.IsAbs(path) && !slices.Contains(strings.Split(path, string(filepath.Separator)), "..") &&
		within(resolveExisting(filepath.Clean(path)), resolveExisting(root)) {
		reason += " (it is inside the repository, where the command already runs: write it relative to the repository root)"
	}
	return reason
}

func commandArgumentRisk(argv []string, root string) string {
	if len(argv) == 0 {
		return ""
	}
	for _, arg := range argv {
		if path := argumentPath(arg, root, true); path != "" {
			return pathRisk(path, root)
		}
	}
	program := filepath.Base(argv[0])
	if interpreters[program] {
		return "standing approval cannot cover an interpreter's arguments"
	}
	key, args := program, argv[1:]
	if (program == "git" || program == "go") && len(args) > 0 {
		key, args = key+" "+args[0], args[1:]
	}
	flags, known := standingFlags[key]
	if (program == "git" || program == "go") && !known {
		return "standing approval has no reviewed argument rules for " + key
	}
	operands := false
	targets := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		// Make assignments are interpreted even after -- and after a target.
		if program == "make" && !strings.HasPrefix(arg, "-") && strings.Contains(arg, "=") {
			return "standing approval cannot cover a make variable assignment: " + arg
		}
		if operands || !strings.HasPrefix(arg, "-") || arg == "-" {
			if program == "make" {
				targets++
				if targets > 1 {
					return "standing approval cannot cover additional make targets"
				}
			}
			continue
		}
		if arg == "--" {
			// Go passes later flags to its test binary, whose flag handling
			// is project-defined. It does not end option parsing as in git.
			if program == "go" {
				return "standing approval cannot cover arguments passed to a test program"
			}
			operands = true
			continue
		}
		flag, value, joined := strings.Cut(arg, "=")
		if !joined && listed(flags.plain, flag) {
			continue
		}
		if listed(flags.value, flag) {
			if !joined {
				if i+1 == len(args) || strings.HasPrefix(args[i+1], "-") {
					return "standing approval requires an explicit value for " + flag
				}
				i++
				value = args[i]
			}
			if value == "" {
				return "standing approval requires an explicit value for " + flag
			}
			continue
		}
		// The compact forms used by the project's tools name counts only.
		if key == "git log" && number.MatchString(strings.TrimPrefix(arg, "-")) {
			continue
		}
		if len(arg) > 2 && listed(flags.value, arg[:2]) && strings.Contains(" -n -U -A -B -C -j -m ", " "+arg[:2]+" ") && number.MatchString(arg[2:]) {
			continue
		}
		return "standing approval has no reviewed flag rule for " + key + " " + arg
	}
	return ""
}

// argumentRisk applies only to commands a standing prefix would otherwise
// cover. Other commands retain their ordinary confirmation and hard triggers.
func (c *CommandAllow) argumentRisk(argv []string) string {
	if cmds, wrapped := shellCommands(argv); wrapped {
		if cmds == nil || !c.systemCommand(argv[0]) {
			return ""
		}
		approved := false
		for _, cmd := range cmds {
			approved = approved || c.rawMatch(cmd.words) != ""
		}
		if approved {
			for _, words := range append([]shellCommand{{words: argv[:1]}}, cmds...) {
				for _, arg := range words.words {
					if path := argumentPath(arg, c.root, true); path != "" {
						return pathRisk(path, c.root)
					}
				}
			}
		}
		for _, cmd := range cmds {
			if c.rawMatch(cmd.words) != "" {
				if reason := commandArgumentRisk(cmd.words, c.root); reason != "" {
					return reason
				}
			}
		}
		return ""
	}
	if c.rawMatch(argv) != "" {
		return commandArgumentRisk(argv, c.root)
	}
	return ""
}
