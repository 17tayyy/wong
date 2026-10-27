package dashboard

import "strings"

type lineOp int

const (
	same lineOp = iota
	added
	removed
)

// diffLine is one line of a diff between two versions of a note.
type diffLine struct {
	Op   lineOp
	Text string
}

// diffLines compares two texts line by line with a longest common subsequence.
func diffLines(before, after string) []diffLine {
	a, b := splitLines(before), splitLines(after)
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []diffLine
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, diffLine{same, a[i]})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, diffLine{removed, a[i]})
			i++
		default:
			out = append(out, diffLine{added, b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, diffLine{removed, a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, diffLine{added, b[j]})
	}
	return out
}

// diffStat counts the lines a diff adds and removes.
func diffStat(lines []diffLine) (plus, minus int) {
	for _, l := range lines {
		switch l.Op {
		case added:
			plus++
		case removed:
			minus++
		}
	}
	return plus, minus
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
