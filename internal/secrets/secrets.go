// Package secrets finds credentials in a text by their well-known shapes, after gitleaks' rules, so they stay off the server.
package secrets

import (
	"bytes"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Finding is a credential found in a text: its line, from 1, and what kind it is. It never holds the credential itself.
type Finding struct {
	Line int
	Kind string
}

func (f Finding) String() string {
	return fmt.Sprintf("line %d (%s)", f.Line, f.Kind)
}

// List joins findings for a message: line 3 (GitHub token), line 9 (AWS access key ID).
func List(found []Finding) string {
	parts := make([]string, len(found))
	for i, f := range found {
		parts[i] = f.String()
	}
	return strings.Join(parts, ", ")
}

type rule struct {
	kind         string
	re           *regexp.Regexp
	bits         float64
	loose, named bool
}

const assigned = `["'` + "`" + `]?[ \t]*(?:=|:=|=>|:)[ \t]*["'` + "`" + `]?`

var rules = []rule{
	{kind: "private key", bits: 3.5,
		re: regexp.MustCompile(`-----BEGIN[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----(?P<s>(?s:.){64,}?)-----END[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----`)},
	{kind: "AWS access key ID", bits: 2,
		re: regexp.MustCompile(`\b(?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)(?P<s>[A-Z2-7]{16})\b`)},
	{kind: "GitHub token", bits: 3,
		re: regexp.MustCompile(`\bgh[pousr]_(?P<s>[0-9A-Za-z]{36})\b`)},
	{kind: "GitHub token", bits: 3,
		re: regexp.MustCompile(`\bgithub_pat_(?P<s>[0-9A-Za-z_]{82})\b`)},
	{kind: "Stripe API key", bits: 3,
		re: regexp.MustCompile(`\b[rs]k_(?:live|test|prod)_(?P<s>[0-9A-Za-z]{20,247})\b`)},
	{kind: "Stripe webhook secret", bits: 3,
		re: regexp.MustCompile(`\bwhsec_(?P<s>[0-9A-Za-z+/=]{24,})`)},
	{kind: "Slack token", bits: 3,
		re: regexp.MustCompile(`\bxox[abeoprs](?:\.xox[bp])?-(?P<s>[0-9][0-9A-Za-z-]{19,})`)},
	{kind: "Slack token", bits: 3,
		re: regexp.MustCompile(`\bxapp-[0-9]-(?P<s>[0-9A-Za-z-]{20,})`)},
	{kind: "Anthropic API key", bits: 3,
		re: regexp.MustCompile(`\bsk-ant-[a-z]{2,8}[0-9]{2}-(?P<s>[0-9A-Za-z_-]{32,})`)},
	{kind: "OpenAI API key", bits: 3, loose: true,
		re: regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-)?(?P<s>[0-9A-Za-z_-]{32,})`)},
	{kind: "Google API key", bits: 3,
		re: regexp.MustCompile(`\bAIza(?P<s>[0-9A-Za-z_-]{35})(?:[^0-9A-Za-z_-]|$)`)},
	{kind: "JSON Web Token", bits: 3,
		re: regexp.MustCompile(`\beyJ[0-9A-Za-z_-]{10,}\.eyJ[0-9A-Za-z_-]{10,}\.(?P<s>[0-9A-Za-z_-]{20,})`)},
	{kind: "Linear API key", bits: 3,
		re: regexp.MustCompile(`\blin_(?:api|oauth)_(?P<s>[0-9A-Za-z]{40,64})\b`)},
	{kind: "wong token", bits: 3,
		re: regexp.MustCompile(`\bwong_(?:[a-z0-9][a-z0-9-]{0,62}_)?(?P<s>[0-9a-f]{64})\b`)},
	{kind: "bearer token", bits: 2.5, loose: true,
		re: regexp.MustCompile(`(?i)\bbearer[ \t]+(?P<s>[0-9a-z._~+/-]{20,}=*)`)},
	{kind: "password in a URL", bits: 2, loose: true,
		re: regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,20}://[^\s:/?#@]{0,64}:(?P<s>[^\s/?#@]{8,128})@`)},
	{bits: 2.5, loose: true, named: true,
		re: regexp.MustCompile(`(?i)\b(?P<name>[a-z0-9_.-]{0,40}(?:pass(?:wd|word|phrase)?|secret|token|api[_.-]?key|access[_.-]?key|private[_.-]?key|[_.-]key|credentials?)[a-z0-9_-]{0,20})` +
			assigned + `(?P<s>[0-9a-z+/=_.~!@#%^&*$-]{12,200})(?:["'` + "`" + `\s,;)}\]]|$)`)},
}

var (
	placeholderWords = []string{"example", "sample", "dummy", "placeholder", "changeme", "redacted"}
	madeUpWords      = []string{"your", "xxxx", "fake"}
)

// Scan returns the credentials in a text, by line.
func Scan(text []byte) []Finding {
	var found []Finding
	var taken [][2]int
	for _, r := range rules {
		s, name := r.re.SubexpIndex("s"), r.re.SubexpIndex("name")
		for _, m := range r.re.FindAllSubmatchIndex(text, -1) {
			at := [2]int{m[2*s], m[2*s+1]}
			if slices.ContainsFunc(taken, func(t [2]int) bool { return at[0] < t[1] && t[0] < at[1] }) || !credible(r, text[at[0]:at[1]]) {
				continue
			}
			taken = append(taken, at)
			kind := r.kind
			if r.named {
				kind = "value of " + string(text[m[2*name]:m[2*name+1]])
			}
			f := Finding{Line: 1 + bytes.Count(text[:m[0]], []byte("\n")), Kind: kind}
			if !slices.Contains(found, f) {
				found = append(found, f)
			}
		}
	}
	slices.SortStableFunc(found, func(a, b Finding) int { return a.Line - b.Line })
	return found
}

func credible(r rule, secret []byte) bool {
	s := strings.Join(strings.Fields(string(secret)), "")
	return !placeholder(s, r.loose) && entropy(s) >= r.bits && (!r.loose || mixed(s))
}

func placeholder(s string, loose bool) bool {
	if strings.ContainsAny(s, "<>{}") || strings.HasPrefix(s, "$") || strings.Contains(s, "...") || run(s) >= 8 {
		return true
	}
	if len(s) > 64 {
		return false
	}
	words := placeholderWords
	if loose {
		words = append(words[:len(words):len(words)], madeUpWords...)
	}
	for _, w := range words {
		for _, form := range []string{w, strings.ToUpper(w), strings.ToUpper(w[:1]) + w[1:]} {
			if strings.Contains(s, form) {
				return true
			}
		}
	}
	return false
}

// run returns the longest stretch of characters that count up or down one at a time, as in abcdefgh or 98765432.
func run(s string) int {
	return max(streak(s, 1), streak(s, -1))
}

func streak(s string, step int) int {
	longest, n := min(len(s), 1), 1
	for i := 1; i < len(s); i++ {
		if int(s[i])-int(s[i-1]) == step {
			n++
		} else {
			n = 1
		}
		longest = max(longest, n)
	}
	return longest
}

func entropy(s string) float64 {
	var counts [256]int
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	var h float64
	for _, c := range counts {
		if c > 0 {
			p := float64(c) / float64(len(s))
			h -= p * math.Log2(p)
		}
	}
	return h
}

func mixed(s string) bool {
	for _, word := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(word) >= 8 && strings.ContainsFunc(word, unicode.IsLetter) && strings.ContainsFunc(word, unicode.IsDigit) {
			return true
		}
	}
	return false
}
