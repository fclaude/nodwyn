package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// baseCommit is what the change is measured against: where the branch left
// its upstream, so work committed but not yet pushed counts too, or HEAD
// when the branch has no upstream.
func baseCommit() (commit, against string, err error) {
	if up, err := git("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil && up != "" {
		if mb, err := git("merge-base", "HEAD", up); err == nil {
			return mb, up, nil
		}
	}
	head, err := git("rev-parse", "HEAD")
	return head, "HEAD", err
}

// changedFiles lists every path that differs from base: committed since,
// staged, unstaged or deleted, and new files git does not ignore.
func changedFiles(base string) ([]string, error) {
	diff, err := git("diff", "--name-only", "--no-renames", base)
	if err != nil {
		return nil, err
	}
	untracked, err := git("ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(diff+"\n"+untracked, "\n") {
		if p = strings.TrimSpace(p); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

var hunk = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// changedLines reports which lines of path, as it is now, differ from base;
// isNew when base has no such file, so every line is new. A hunk that only
// deletes counts as the line where the deletion was, so a test that lost a
// line still counts as changed.
func changedLines(base, path string) (ranges [][2]int, isNew bool, err error) {
	if _, err := git("cat-file", "-e", base+":"+path); err != nil {
		return nil, true, nil
	}
	out, err := git("diff", "-U0", "--no-renames", base, "--", path)
	if err != nil {
		return nil, false, err
	}
	for _, line := range strings.Split(out, "\n") {
		m := hunk.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		start, _ := strconv.Atoi(m[1])
		count := 1
		if m[2] != "" {
			count, _ = strconv.Atoi(m[2])
		}
		end := start + count - 1
		if count == 0 {
			end = start + 1
		}
		ranges = append(ranges, [2]int{start, end})
	}
	return ranges, false, nil
}
