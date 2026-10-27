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

var (
	frontmatterRE   = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n?`)
	scopeRE         = regexp.MustCompile(`\A(global|project:[a-z0-9][a-z0-9._-]*)\z`)
	unquotedColonRE = regexp.MustCompile(`(?m)^([A-Za-z_][\w-]*: )([^\s"'\[{|>&*!#].*: .*?)[ \t]*$`)
	yamlQuoter      = strings.NewReplacer(`\`, `\\`, `"`, `\"`)
)

type Note struct {
	Name        string
	Description string
	Body        string
	Kind        Kind
	Scope       string
	Sources     []string
	Verified    time.Time
	// Extra holds the other frontmatter entries as alternating key and value nodes, in file order.
	Extra []*yaml.Node
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
