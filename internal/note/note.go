// Package note reads and writes memory notes: Claude Code's frontmatter plus wong's fields.
package note

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Kind string

const (
	Preference Kind = "preference"
	Project    Kind = "project"
	Person     Kind = "person"
	Client     Kind = "client"
	Decision   Kind = "decision"
	Reference  Kind = "reference"
)

var kinds = []Kind{Preference, Project, Person, Client, Decision, Reference}

var claudeTypes = map[Kind]string{
	Preference: "feedback",
	Project:    "project",
	Decision:   "project",
	Reference:  "reference",
	Person:     "reference",
	Client:     "reference",
}

// KindOf returns the kind that Claude Code's own memory type stands for, or "" for an unknown type.
func KindOf(claudeType string) Kind {
	switch claudeType {
	case "user", "feedback":
		return Preference
	case "project":
		return Project
	case "reference":
		return Reference
	}
	return ""
}

// ParseKind returns the kind named s.
func ParseKind(s string) (Kind, error) {
	if k := Kind(s); slices.Contains(kinds, k) {
		return k, nil
	}
	return "", fmt.Errorf("unknown kind %q: use preference, project, person, client, decision or reference", s)
}

// CheckPending refuses a pending value other than release and main.
func CheckPending(s string) error {
	if s == "" || s == "release" || s == "main" {
		return nil
	}
	return fmt.Errorf("unknown pending %q: use release (the next v* tag) or main (the next push to the main branch)", s)
}

// Local is the visibility of a note that stays on its machine.
const Local = "local"

// CheckVisibility refuses a visibility other than local.
func CheckVisibility(s string) error {
	if s == "" || s == Local {
		return nil
	}
	return fmt.Errorf("unknown visibility %q: use local to keep the note on this machine, or leave it out", s)
}

// KeptLocal reports whether a note's file says visibility: local, read on its own so a frontmatter broken elsewhere still keeps the note home.
func KeptLocal(data []byte) (bool, error) {
	data = bytes.ReplaceAll(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), []byte("\r\n"), []byte("\n"))
	m := frontmatterRE.FindSubmatch(data)
	if m == nil {
		return false, nil
	}
	var v string
	if root, err := loadYAML(m[1]); err == nil {
		node := lookup(root.Content, "visibility")
		if meta := lookup(root.Content, "metadata"); node == nil && meta != nil && meta.Kind == yaml.MappingNode {
			node = lookup(meta.Content, "visibility")
		}
		if node == nil {
			return false, nil
		}
		if node.Kind != yaml.ScalarNode {
			return false, errors.New("visibility must be a string: use local to keep the note on this machine, or leave it out")
		}
		v = node.Value
	} else if f := visibilityRE.FindSubmatch(m[1]); f != nil {
		v = strings.Trim(string(f[1]), `"'`)
	}
	if err := CheckVisibility(v); err != nil {
		return false, err
	}
	return v == Local, nil
}

// WikiLinkRE matches a [[link]] to another note: the name, then an optional #heading and |label.
var WikiLinkRE = regexp.MustCompile(`\[\[([^\]|#]+)(?:#[^\]|]*)?(?:\|([^\]]+))?\]\]`)

var codeRE = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~|`[^`\n]*`")

// WithoutCode blanks a markdown text's code blocks and inline code, where a [[link]] is text, as in Obsidian.
func WithoutCode(text string) string {
	return codeRE.ReplaceAllString(text, " ")
}

// OutsideCode applies f to the parts of a markdown text that are not code and keeps the code as it is.
func OutsideCode(text string, f func(string) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range codeRE.FindAllStringIndex(text, -1) {
		b.WriteString(f(text[last:loc[0]]))
		b.WriteString(text[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(f(text[last:]))
	return b.String()
}

var (
	frontmatterRE   = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n?`)
	scopeRE         = regexp.MustCompile(`\A(global|project:[a-z0-9][a-z0-9._-]*)\z`)
	visibilityRE    = regexp.MustCompile(`(?m)^visibility:[ \t]*(.*?)[ \t]*$`)
	unquotedColonRE = regexp.MustCompile(`(?m)^([A-Za-z_][\w-]*: )([^\s"'\[{|>&*!#].*: .*?)[ \t]*$`)
	yamlQuoter      = strings.NewReplacer(`\`, `\\`, `"`, `\"`)
)

type Note struct {
	Name        string
	Description string
	Body        string
	Kind        Kind
	Scope       string
	Tags        []string
	// Pending is "release" or "main" when the note leaves work for the next release or push to the main branch.
	Pending string
	// Visibility is "local" when the note stays on its machine: never pushed to the server, nor its deletion, and never overwritten by a pull.
	Visibility string
	Sources    []string
	Verified   time.Time
	// Extra holds the other frontmatter entries as alternating key and value nodes, in file order.
	Extra []*yaml.Node
}

// New returns a note with the metadata.type that Claude Code's memory expects for its kind.
func New(name, description string, kind Kind, scope, body string) *Note {
	meta := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{str("type"), str(claudeTypes[kind])}}
	return &Note{
		Name:        name,
		Description: description,
		Body:        body,
		Kind:        kind,
		Scope:       scope,
		Extra:       []*yaml.Node{str("metadata"), meta},
	}
}

func Parse(data []byte) (*Note, error) {
	m := frontmatterRE.FindSubmatchIndex(data)
	if m == nil {
		return nil, errors.New("missing frontmatter")
	}
	root, err := loadYAML(data[m[2]:m[3]])
	if err != nil {
		return nil, err
	}
	n := &Note{Body: string(data[m[1]:])}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		var err error
		switch key.Value {
		case "name":
			n.Name, err = text(value, "name")
		case "description":
			n.Description, err = text(value, "description")
		case "kind":
			n.Kind, err = kind(value)
		case "scope":
			n.Scope, err = scope(value)
		case "tags":
			n.Tags = tags(value)
		case "pending":
			n.Pending = value.Value
		case "visibility":
			n.Visibility = value.Value
		case "sources":
			n.Sources, err = sources(value)
		case "verified":
			n.Verified, err = verified(value)
		default:
			n.Extra = append(n.Extra, key, value)
		}
		if err != nil {
			return nil, err
		}
	}
	if n.Name == "" {
		return nil, errors.New("name must be a non-empty string")
	}
	if n.Description == "" {
		return nil, errors.New("description must be a non-empty string")
	}
	return n, nil
}

func ProjectScope(id string) (string, error) {
	s := "project:" + id
	if !scopeRE.MatchString(s) {
		return "", fmt.Errorf("invalid project id %q: use lowercase letters, digits, '.', '_' or '-'", id)
	}
	return s, nil
}

func Load(path string) (*Note, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	n, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return n, nil
}

func (n *Note) Marshal() ([]byte, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}
	add := func(key string, value *yaml.Node) {
		root.Content = append(root.Content, str(key), value)
	}
	add("name", str(n.Name))
	add("description", str(n.Description))
	root.Content = append(root.Content, n.Extra...)
	if n.Kind != "" {
		add("kind", str(string(n.Kind)))
	}
	if n.Scope != "" {
		add("scope", str(n.Scope))
	}
	if len(n.Tags) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, t := range n.Tags {
			seq.Content = append(seq.Content, str(t))
		}
		add("tags", seq)
	}
	if n.Pending != "" {
		add("pending", str(n.Pending))
	}
	if n.Visibility != "" {
		add("visibility", str(n.Visibility))
	}
	if len(n.Sources) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, s := range n.Sources {
			seq.Content = append(seq.Content, str(s))
		}
		add("sources", seq)
	}
	if !n.Verified.IsZero() {
		add("verified", &yaml.Node{Kind: yaml.ScalarNode, Value: n.Verified.Format(time.DateOnly)})
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	buf.WriteString("---\n")
	buf.WriteString(n.Body)
	return buf.Bytes(), nil
}

// Alias makes the note's name an Obsidian alias when its file is named otherwise, so a [[name]] link resolves there too.
func (n *Note) Alias(stem string) {
	if n.Name == stem {
		return
	}
	aliases := lookup(n.Extra, "aliases")
	if aliases == nil {
		aliases = &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		n.Extra = append(n.Extra, str("aliases"), aliases)
	}
	if aliases.Kind != yaml.SequenceNode {
		return
	}
	for _, a := range aliases.Content {
		if a.Value == n.Name {
			return
		}
	}
	aliases.Content = append(aliases.Content, str(n.Name))
}

// ClaudeType returns Claude Code's own type: metadata.type, or the legacy top-level type.
func (n *Note) ClaudeType() string {
	if t := n.meta("type"); t != nil {
		return t.Value
	}
	if t := lookup(n.Extra, "type"); t != nil && t.Kind == yaml.ScalarNode {
		return t.Value
	}
	return ""
}

// Modified returns Claude Code's metadata.modified, or the zero time.
func (n *Note) Modified() time.Time {
	if m := n.meta("modified"); m != nil {
		if t, err := time.Parse(time.RFC3339, m.Value); err == nil {
			return t
		}
	}
	return time.Time{}
}

// modifiedSlack is how far metadata.modified may run ahead of the file's own date.
const modifiedSlack = time.Minute

// LastWrite is when a note was last written: its file's date, or metadata.modified when it runs ahead of it by no more than a minute.
// It reports false when metadata.modified claims a later write than the file had, which wong does not believe.
func (n *Note) LastWrite(file time.Time) (time.Time, bool) {
	m := n.Modified()
	switch {
	case file.IsZero():
		return m, true
	case !m.After(file):
		return file, true
	case m.Sub(file) <= modifiedSlack:
		return m, true
	}
	return file, false
}

func (n *Note) meta(key string) *yaml.Node {
	if meta := lookup(n.Extra, "metadata"); meta != nil && meta.Kind == yaml.MappingNode {
		return lookup(meta.Content, key)
	}
	return nil
}

func lookup(pairs []*yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i].Value == key {
			return pairs[i+1]
		}
	}
	return nil
}

func loadYAML(src []byte) (*yaml.Node, error) {
	root, err := decodeMapping(src)
	if err == nil {
		return root, nil
	}
	quoted := unquotedColonRE.ReplaceAllFunc(src, func(line []byte) []byte {
		m := unquotedColonRE.FindSubmatch(line)
		return fmt.Appendf(nil, `%s"%s"`, m[1], yamlQuoter.Replace(string(m[2])))
	})
	if root, err := decodeMapping(quoted); err == nil {
		return root, nil
	}
	return nil, err
}

func decodeMapping(src []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("invalid frontmatter: %w", err)
	}
	if len(doc.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode}, nil
	}
	if root := doc.Content[0]; root.Kind == yaml.MappingNode {
		return root, nil
	}
	return nil, errors.New("frontmatter is not a mapping")
}

func str(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

func isString(v *yaml.Node) bool {
	return v.Kind == yaml.ScalarNode && v.ShortTag() == "!!str" && strings.TrimSpace(v.Value) != ""
}

func text(v *yaml.Node, key string) (string, error) {
	if !isString(v) {
		return "", fmt.Errorf("%s must be a non-empty string", key)
	}
	return v.Value, nil
}

func kind(v *yaml.Node) (Kind, error) {
	if k := Kind(v.Value); isString(v) && slices.Contains(kinds, k) {
		return k, nil
	}
	return "", fmt.Errorf("unknown kind: %q", v.Value)
}

func scope(v *yaml.Node) (string, error) {
	if !isString(v) || !scopeRE.MatchString(v.Value) {
		return "", fmt.Errorf("scope must be 'global' or 'project:<id>', got %q", v.Value)
	}
	return v.Value, nil
}

// tags reads Obsidian's tags property, a list or one string of comma- or space-separated tags, leaving any # out.
func tags(v *yaml.Node) []string {
	var raw []string
	switch v.Kind {
	case yaml.SequenceNode:
		for _, t := range v.Content {
			if t.Kind == yaml.ScalarNode {
				raw = append(raw, t.Value)
			}
		}
	case yaml.ScalarNode:
		raw = strings.FieldsFunc(v.Value, func(r rune) bool { return r == ',' || r == ' ' })
	}
	var out []string
	for _, t := range raw {
		if t = strings.TrimPrefix(strings.TrimSpace(t), "#"); t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

func sources(v *yaml.Node) ([]string, error) {
	err := errors.New("sources must be a list of strings")
	if v.Kind != yaml.SequenceNode {
		return nil, err
	}
	out := make([]string, 0, len(v.Content))
	for _, s := range v.Content {
		if !isString(s) {
			return nil, err
		}
		out = append(out, s.Value)
	}
	return out, nil
}

func verified(v *yaml.Node) (time.Time, error) {
	if v.Kind == yaml.ScalarNode && (v.ShortTag() == "!!timestamp" || v.ShortTag() == "!!str") {
		if t, err := time.Parse(time.DateOnly, v.Value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("verified must be a date (YYYY-MM-DD), got %q", v.Value)
}
