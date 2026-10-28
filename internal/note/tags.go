package note

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	tagRE   = regexp.MustCompile(`^[\p{Ll}\p{N}][\p{Ll}\p{N}_-]*(/[\p{Ll}\p{N}][\p{Ll}\p{N}_-]*)*$`)
	digitRE = regexp.MustCompile(`^[\p{N}/_-]+$`)
	spaceRE = regexp.MustCompile(`\s+`)
)

// NormalizeTag writes a tag the way wong keeps them: lowercase, words joined by hyphens, nested with /, without #.
func NormalizeTag(t string) (string, error) {
	norm := spaceRE.ReplaceAllString(strings.ToLower(strings.TrimPrefix(strings.TrimSpace(t), "#")), "-")
	if !tagRE.MatchString(norm) || digitRE.MatchString(norm) {
		return "", fmt.Errorf("tag %q: use letters and digits, words joined by -, nested with /, and at least one letter", t)
	}
	return norm, nil
}

// stem drops hyphens and the usual plural and derived endings, so spellings of one tag compare equal.
func stem(t string) string {
	t = strings.NewReplacer("-", "", "_", "").Replace(t)
	switch {
	case strings.HasSuffix(t, "ies") && len(t) > 5:
		t = t[:len(t)-3] + "y"
	case strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") && len(t) > 3:
		t = t[:len(t)-1]
	}
	for _, suffix := range []string{"ment", "ing", "ion", "ed"} {
		if strings.HasSuffix(t, suffix) && len(t)-len(suffix) >= 4 {
			t = t[:len(t)-len(suffix)]
			break
		}
	}
	if strings.HasSuffix(t, "e") && len(t) >= 5 {
		t = t[:len(t)-1]
	}
	return t
}

// CloseTags reports whether two different tags are spellings of one: the same stem, or one letter apart on long ones.
func CloseTags(a, b string) bool {
	if a == b {
		return false
	}
	sa, sb := stem(a), stem(b)
	return sa == sb || len(sa) >= 6 && len(sb) >= 6 && Distance(sa, sb) <= 1
}

// Distance is the number of letters to change, add or drop to turn a into b.
func Distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

var attachmentRE = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|svg|webp|avif|bmp|pdf|mp3|mp4|m4a|wav|ogg|webm|mov|zip|csv|xlsx?|docx?|pptx?|canvas|excalidraw)$`)

// IsAttachment reports whether a [[link]] names a file Obsidian embeds, such as an image or a PDF, rather than a note.
func IsAttachment(name string) bool {
	return attachmentRE.MatchString(strings.TrimSpace(name))
}
