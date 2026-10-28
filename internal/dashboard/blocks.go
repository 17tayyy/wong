package dashboard

import (
	"html/template"
	"regexp"
	"slices"
	"strings"
)

// block is a piece of a note's body edited on its own: lines From up to To, and the markdown they hold.
type block struct {
	From, To int
	Src      string
	HTML     template.HTML
}

var (
	fenceRE    = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	listItemRE = regexp.MustCompile(`^ {0,3}([-*+]|\d{1,9}[.)])(\s|$)`)
)

// splitBlocks cuts a body at blank lines, keeping fenced code, lists and indented code whole.
func splitBlocks(body string) []block {
	lines := strings.Split(body, "\n")
	var blocks []block
	start, fence := -1, ""
	flush := func(end int) {
		for end > start && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		if start >= 0 && end > start {
			blocks = append(blocks, block{From: start, To: end, Src: strings.Join(lines[start:end], "\n")})
		}
		start = -1
	}
	for i, line := range lines {
		switch {
		case fence != "":
			if m := fenceRE.FindString(line); m != "" && strings.HasPrefix(strings.TrimLeft(m, " "), fence) && strings.TrimSpace(line[len(m):]) == "" {
				fence = ""
			}
		case strings.TrimSpace(line) == "":
			if start >= 0 && !continues(lines, start, i) {
				flush(i)
			}
		default:
			if start < 0 {
				start = i
			}
			if m := fenceRE.FindStringSubmatch(line); m != nil {
				fence = m[1]
			}
		}
	}
	if start >= 0 {
		flush(len(lines))
	}
	return blocks
}

// continues reports whether a list or indented code that starts at line start goes on past the blank line at i.
func continues(lines []string, start, i int) bool {
	j := i
	for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
		j++
	}
	if j == len(lines) {
		return false
	}
	next := lines[j]
	indented := strings.HasPrefix(next, "  ") || strings.HasPrefix(next, "\t")
	switch {
	case listItemRE.MatchString(lines[start]):
		return listItemRE.MatchString(next) || indented
	case strings.HasPrefix(lines[start], "    ") || strings.HasPrefix(lines[start], "\t"):
		return strings.HasPrefix(next, "    ") || strings.HasPrefix(next, "\t")
	}
	return false
}

// renderBlocks splits a body into blocks and renders each one.
func renderBlocks(body string, resolve func(name string) (string, bool)) ([]block, error) {
	blocks := splitBlocks(body)
	for i := range blocks {
		html, err := renderMarkdown(blocks[i].Src, resolve)
		if err != nil {
			return nil, err
		}
		blocks[i].HTML = html
	}
	return blocks, nil
}

// spliceBody puts text in place of lines from up to to of a body, or after its last line when from is -1.
func spliceBody(body string, from, to int, text string) (string, bool) {
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if from < 0 {
		if strings.TrimSpace(text) == "" {
			return body, true
		}
		if rest := strings.TrimRight(body, "\n"); strings.TrimSpace(rest) != "" {
			return rest + "\n\n" + text + "\n", true
		}
		return strings.Repeat("\n", min(1, strings.Count(body, "\n"))) + text + "\n", true
	}
	lines := strings.Split(body, "\n")
	if from > to || to > len(lines) {
		return "", false
	}
	var repl []string
	if strings.TrimSpace(text) != "" {
		repl = strings.Split(text, "\n")
	}
	out := slices.Concat(lines[:from], repl, lines[to:])
	if repl == nil && from > 0 && from < len(out) && strings.TrimSpace(out[from-1]) == "" && strings.TrimSpace(out[from]) == "" {
		out = slices.Delete(out, from, from+1)
	}
	s := strings.Join(out, "\n")
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s, true
}
