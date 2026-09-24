// Package textdiff produces unified diffs using the Myers algorithm.
package textdiff

import (
	"fmt"
	"strings"
)

type op struct {
	kind byte // ' ', '-', '+'
	text string
	a, b int // 0-based line positions in a and b before this op
}

// maxEdit caps the work done on pathological inputs.
const maxEdit = 4000

// Unified returns a unified diff from a to b, or "" when they are equal.
func Unified(aName, bName, a, b string, context int) string {
	if a == b {
		return ""
	}
	al, bl := split(a), split(b)
	ops, ok := myers(al, bl)
	if !ok {
		return fmt.Sprintf("--- %s\n+++ %s\n(files differ; too many changes to display)\n", aName, bName)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", aName, bName)
	for _, h := range hunks(ops, context) {
		sb.WriteString(h)
	}
	return sb.String()
}

// Stat counts added and removed lines between a and b.
func Stat(a, b string) (added, removed int) {
	if a == b {
		return 0, 0
	}
	ops, ok := myers(split(a), split(b))
	if !ok {
		return len(split(b)), len(split(a))
	}
	for _, o := range ops {
		switch o.kind {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	return
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func myers(a, b []string) ([]op, bool) {
	n, m := len(a), len(b)
	max := n + m
	if max == 0 {
		return nil, true
	}
	off := max
	v := make([]int, 2*max+2)
	var trace [][]int
	for d := 0; d <= max; d++ {
		if d > maxEdit {
			return nil, false
		}
		snap := make([]int, len(v))
		copy(snap, v)
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(trace, a, b, off), true
			}
		}
	}
	return nil, false
}

func backtrack(trace [][]int, a, b []string, off int) []op {
	x, y := len(a), len(b)
	var rev []op
	for d := len(trace) - 1; d >= 0; d-- {
		v := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := v[off+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			rev = append(rev, op{' ', a[x-1], x - 1, y - 1})
			x--
			y--
		}
		if d > 0 {
			if x == prevX {
				rev = append(rev, op{'+', b[y-1], x, y - 1})
			} else {
				rev = append(rev, op{'-', a[x-1], x - 1, y})
			}
		}
		x, y = prevX, prevY
	}
	ops := make([]op, len(rev))
	for i := range rev {
		ops[i] = rev[len(rev)-1-i]
	}
	return ops
}

func hunks(ops []op, ctx int) []string {
	var changes []int
	for i, o := range ops {
		if o.kind != ' ' {
			changes = append(changes, i)
		}
	}
	var out []string
	for i := 0; i < len(changes); {
		start := changes[i]
		end := changes[i]
		j := i + 1
		for j < len(changes) && changes[j]-end <= 2*ctx+1 {
			end = changes[j]
			j++
		}
		lo := start - ctx
		if lo < 0 {
			lo = 0
		}
		hi := end + ctx + 1
		if hi > len(ops) {
			hi = len(ops)
		}
		var body strings.Builder
		aCount, bCount := 0, 0
		for _, o := range ops[lo:hi] {
			line := o.text
			body.WriteByte(o.kind)
			body.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				body.WriteString("\n\\ No newline at end of file\n")
			}
			if o.kind != '+' {
				aCount++
			}
			if o.kind != '-' {
				bCount++
			}
		}
		aStart, bStart := ops[lo].a, ops[lo].b
		if aCount > 0 {
			aStart++
		}
		if bCount > 0 {
			bStart++
		}
		out = append(out, fmt.Sprintf("@@ -%d,%d +%d,%d @@\n%s", aStart, aCount, bStart, bCount, body.String()))
		i = j
	}
	return out
}
