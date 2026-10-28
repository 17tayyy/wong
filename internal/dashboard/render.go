package dashboard

import (
	"bytes"
	"html/template"
	"net/url"
	"strings"

	"github.com/17tayyy/wong/internal/note"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

var (
	markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))
	wikiRE   = note.WikiLinkRE
)

// renderMarkdown renders a note's body as HTML, turning [[links]] that resolve to a note into links to it; raw HTML in the note is dropped.
func renderMarkdown(body string, resolve func(name string) (string, bool)) (template.HTML, error) {
	body = wikiRE.ReplaceAllStringFunc(body, func(m string) string {
		sub := wikiRE.FindStringSubmatch(m)
		name, label := strings.TrimSpace(sub[1]), strings.TrimSpace(sub[2])
		if label == "" {
			label = name
		}
		label = strings.NewReplacer("[", `\[`, "]", `\]`).Replace(label)
		if path, ok := resolve(name); ok {
			return "[" + label + "](" + notePath(path) + ")"
		}
		return label
	})
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(body), &buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// notePath is the dashboard URL of a note of the brain.
func notePath(path string) string {
	return "/notes/" + (&url.URL{Path: path}).EscapedPath()
}
