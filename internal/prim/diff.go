package prim

import (
	"fmt"
	"strings"
)

// Unified renders a unified diff of a → b with n lines of context. It is
// used for fs-write confirmation prompts, so the user sees exactly what
// changes before saying yes. Inputs above maxDiffLines fall back to a
// summary.
func Unified(name, a, b string, n int) string {
	al := splitLines(a)
	bl := splitLines(b)
	if len(al)+len(bl) > maxDiffLines {
		return fmt.Sprintf("--- %s (%d lines)\n+++ %s (%d lines)\n(diff too large to show)\n", name, len(al), name, len(bl))
	}
	ops := myers(al, bl)
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

const maxDiffLines = 6000

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

// myers computes a shortest edit script (Myers 1986, forward-only with
// full trace) — fine for the file sizes a confirm prompt shows.
func myers(a, b []string) []op {
	n, m := len(a), len(b)
	maxD := n + m
	if maxD == 0 {
		return nil
	}
	offset := maxD
	v := make([]int, 2*maxD+2)
	var trace [][]int
	found := false
	for d := 0; d <= maxD && !found; d++ {
		snapshot := make([]int, len(v))
		copy(snapshot, v)
		trace = append(trace, snapshot)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}
	// Backtrack.
	var ops []op
	x, y := n, m
	for d := len(trace) - 1; d >= 0; d-- {
		vv := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && vv[offset+k-1] < vv[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vv[offset+prevK]
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
	return ops
}
