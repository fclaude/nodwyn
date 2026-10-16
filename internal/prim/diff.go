package prim

import (
	"fmt"
	"strings"
)

// Unified renders a unified diff of a → b with n lines of context. It is
// used for fs-write confirmation prompts, so the user sees exactly what
// changes before saying yes. Inputs above maxDiffLines, and changes of more
// than maxDiffEdits lines, fall back to a summary.
func Unified(name, a, b string, n int) string {
	al := splitLines(a)
	bl := splitLines(b)
	ops, ok := diffOps(al, bl)
	if !ok {
		return fmt.Sprintf("--- %s (%d lines)\n+++ %s (%d lines)\n(diff too large to show)\n", name, len(al), name, len(bl))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", name, name)
	// Group ops into hunks separated by more than 2n unchanged lines.
	i := 0
	for i < len(ops) {
		// skip leading equal run
		for i < len(ops) && ops[i].kind == ' ' {
			i++
		}
		if i >= len(ops) {
			break
		}
		start := max(0, i-n)
		end := i
		for end < len(ops) {
			// extend through changes and short equal runs
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := 0
			for end+run < len(ops) && ops[end+run].kind == ' ' {
				run++
			}
			if run > 2*n || end+run >= len(ops) {
				end += min(run, n)
				break
			}
			end += run
		}
		writeHunk(&sb, ops[start:end])
		i = end
	}
	return sb.String()
}

const (
	maxDiffLines = 6000
	// maxDiffEdits bounds how many lines may differ. The search keeps a row
	// of state for each edit, so memory grows with its square: 2,500 edits
	// take about 13 MB, where a full rewrite of two 3,000-line files, 6,000
	// edits, took 564 MB.
	maxDiffEdits = 2500
)

type op struct {
	kind byte // ' ', '-', '+'
	line string
	ai   int // 0-based index in a (for ' ' and '-')
	bi   int // 0-based index in b (for ' ' and '+')
}

func writeHunk(sb *strings.Builder, ops []op) {
	if len(ops) == 0 {
		return
	}
	aStart, bStart := -1, -1
	aCount, bCount := 0, 0
	for _, o := range ops {
		switch o.kind {
		case ' ':
			if aStart < 0 {
				aStart, bStart = o.ai, o.bi
			}
			aCount++
			bCount++
		case '-':
			if aStart < 0 {
				aStart, bStart = o.ai, o.bi
			}
			aCount++
		case '+':
			if aStart < 0 {
				aStart, bStart = o.ai, o.bi
			}
			bCount++
		}
	}
	fmt.Fprintf(sb, "@@ -%d,%d +%d,%d @@\n", aStart+1, aCount, bStart+1, bCount)
	for _, o := range ops {
		sb.WriteByte(o.kind)
		sb.WriteString(o.line)
		sb.WriteByte('\n')
	}
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffOps is the edit script from a to b, or false when it is too large to
// compute. The lines the two share at both ends are set aside first: an edit
// in the middle of a long file then costs what it changes, not what the file
// holds.
func diffOps(a, b []string) ([]op, bool) {
	if len(a)+len(b) > maxDiffLines {
		return nil, false
	}
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	middle, ok := myers(a[prefix:len(a)-suffix], b[prefix:len(b)-suffix])
	if !ok {
		return nil, false
	}
	ops := make([]op, 0, prefix+len(middle)+suffix)
	for i := 0; i < prefix; i++ {
		ops = append(ops, op{' ', a[i], i, i})
	}
	for _, o := range middle {
		o.ai += prefix
		o.bi += prefix
		ops = append(ops, o)
	}
	for i := suffix; i > 0; i-- {
		ops = append(ops, op{' ', a[len(a)-i], len(a) - i, len(b) - i})
	}
	return ops, true
}

// myers computes a shortest edit script (Myers 1986, forward-only with a
// trace) in memory that grows with the square of the edits, not with their
// number times the input: each round keeps only the diagonals it can have
// reached. It gives up, false, past maxDiffEdits.
func myers(a, b []string) ([]op, bool) {
	n, m := len(a), len(b)
	maxD := min(n+m, maxDiffEdits)
	if n+m == 0 {
		return nil, true
	}
	offset := maxD + 1
	v := make([]int32, 2*maxD+3)
	// trace[d] holds v before round d for the diagonals -d-1 to d+1.
	trace := make([][]int32, 0, 64)
	found := false
	for d := 0; d <= maxD && !found; d++ {
		trace = append(trace, append([]int32(nil), v[offset-d-1:offset+d+2]...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = int(v[offset+k+1])
			} else {
				x = int(v[offset+k-1]) + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = int32(x)
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}
	if !found {
		return nil, false
	}
	// Backtrack.
	var ops []op
	x, y := n, m
	for d := len(trace) - 1; d >= 0; d-- {
		vv := trace[d]
		at := func(k int) int { return int(vv[k+d+1]) }
		k := x - y
		var prevK int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := at(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			ops = append(ops, op{' ', a[x], x, y})
		}
		if d > 0 {
			if x == prevX {
				y--
				ops = append(ops, op{'+', b[y], x, y})
			} else {
				x--
				ops = append(ops, op{'-', a[x], x, y})
			}
		}
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops, true
}
