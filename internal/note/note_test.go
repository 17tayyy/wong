package note

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const claudeNote = `---
name: project-billing-migration
description: "Billing migration"
metadata:
  node_type: memory
  type: project
  originSessionId: 0b5c7e2a-3f41-4d8e-9a6b-1c2d3e4f5a6b
---

Billing migration implemented in acme-api.
`

const legacyNote = `---
name: Project overview
description: FastAPI shop app
type: project
originSessionId: 7d1e9f20-6a3b-4c5d-8e7f-90a1b2c3d4e5
---
FastAPI app for an online shop.
`

const wongNote = `---
name: eng-76-paused
description: ENG-76 is paused until the app review passes
metadata:
  type: project # kept by wong
kind: decision
scope: project:acme
sources:
  - linear:ENG-76
  - file:acme-api/app/payments/client.py
verified: 2026-10-27
---
Body.
`

func mustParse(t *testing.T, text string) *Note {
	t.Helper()
	n, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return n
}

func mustMarshal(t *testing.T, n *Note) string {
	t.Helper()
	data, err := n.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(data)
}

func extraKeys(n *Note) []string {
	var keys []string
	for i := 0; i < len(n.Extra); i += 2 {
		keys = append(keys, n.Extra[i].Value)
	}
	return keys
}

func TestParseClaudeCodeNoteWithoutWongFields(t *testing.T) {
	n := mustParse(t, claudeNote)

	if n.Name != "project-billing-migration" || n.Description != "Billing migration" {
		t.Errorf("name, description = %q, %q", n.Name, n.Description)
	}
	if got := n.ClaudeType(); got != "project" {
		t.Errorf("ClaudeType() = %q", got)
	}
	if want := "\nBilling migration implemented in acme-api.\n"; n.Body != want {
		t.Errorf("Body = %q", n.Body)
	}
	if n.Kind != "" || n.Scope != "" || n.Sources != nil || !n.Verified.IsZero() {
		t.Errorf("wong fields set: %+v", n)
	}
}

func TestModified(t *testing.T) {
	n := mustParse(t, strings.Replace(claudeNote, "  type: project\n", "  type: project\n  modified: 2026-07-29T22:03:47.507Z\n", 1))

	if want := time.Date(2026, 7, 29, 22, 3, 47, 507e6, time.UTC); !n.Modified().Equal(want) {
		t.Errorf("Modified() = %v", n.Modified())
	}
	if got := mustParse(t, claudeNote).Modified(); !got.IsZero() {
		t.Errorf("Modified() without the field = %v", got)
	}
}

func TestParseLegacyTopLevelType(t *testing.T) {
	n := mustParse(t, legacyNote)

	if got := extraKeys(n); !slices.Equal(got, []string{"type", "originSessionId"}) {
		t.Errorf("extra keys = %v", got)
	}
	if got := n.ClaudeType(); got != "project" {
		t.Errorf("ClaudeType() = %q", got)
	}
}

func TestParseWongFields(t *testing.T) {
	n := mustParse(t, wongNote)

	if n.Kind != Decision || n.Scope != "project:acme" {
		t.Errorf("kind, scope = %q, %q", n.Kind, n.Scope)
	}
	wantSources := []string{"linear:ENG-76", "file:acme-api/app/payments/client.py"}
	if !slices.Equal(n.Sources, wantSources) {
		t.Errorf("Sources = %v", n.Sources)
	}
	if want := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC); !n.Verified.Equal(want) {
		t.Errorf("Verified = %v", n.Verified)
	}
}

func TestMarshalWongNoteUnchanged(t *testing.T) {
	if got := mustMarshal(t, mustParse(t, wongNote)); got != wongNote {
		t.Errorf("Marshal() =\n%s\nwant\n%s", got, wongNote)
	}
}

func TestMarshalDoesNotAddWongFields(t *testing.T) {
	for _, text := range []string{claudeNote, legacyNote} {
		got := mustMarshal(t, mustParse(t, text))

		for _, key := range []string{"kind:", "scope:", "sources:", "verified:"} {
			if strings.Contains(got, key) {
				t.Errorf("Marshal() added %s\n%s", key, got)
			}
		}
		if again := mustMarshal(t, mustParse(t, got)); again != got {
			t.Errorf("Marshal() is not stable:\n%s\n%s", got, again)
		}
	}
}

func TestMarshalKeepsLongValuesOnOneLine(t *testing.T) {
	long := strings.Repeat("palabra ", 40) + "acción"
	got := mustMarshal(t, &Note{Name: "n", Description: long})

	if !strings.Contains(got, "description: "+long+"\n") {
		t.Errorf("Marshal() =\n%s", got)
	}
}

func TestMarshalQuotesStringsThatLookLikeOtherTypes(t *testing.T) {
	n := &Note{Name: "2026", Description: "true", Sources: []string{"null"}}

	if got := mustParse(t, mustMarshal(t, n)); got.Name != "2026" || got.Description != "true" {
		t.Errorf("round trip = %+v", got)
	}
}

func TestParseQuotedVerifiedDate(t *testing.T) {
	n := mustParse(t, strings.Replace(wongNote, "verified: 2026-10-27", "verified: '2026-10-27'", 1))

	if n.Verified.Format(time.DateOnly) != "2026-10-27" {
		t.Errorf("Verified = %v", n.Verified)
	}
}

func TestParseUnquotedColonInTopLevelValue(t *testing.T) {
	n := mustParse(t, "---\nname: commit-style\n"+
		`description: No trailers: no "Co-Authored-By" \o/`+"\n"+
		"metadata:\n  type: feedback\n---\n")

	if want := `No trailers: no "Co-Authored-By" \o/`; n.Description != want {
		t.Errorf("Description = %q", n.Description)
	}
	if got := n.ClaudeType(); got != "feedback" {
		t.Errorf("ClaudeType() = %q", got)
	}
	if again := mustParse(t, mustMarshal(t, n)); again.Description != n.Description {
		t.Errorf("round trip Description = %q", again.Description)
	}
}

func TestParseRejectsInvalidNotes(t *testing.T) {
	const head = "---\nname: n\ndescription: d\n"
	tests := []struct{ text, err string }{
		{"no frontmatter", "missing frontmatter"},
		{"---\n- a\n---\n", "not a mapping"},
		{"---\nname: [\n---\n", "invalid frontmatter"},
		{"---\ndescription: d\n---\n", "name must be"},
		{"---\nname: n\n---\n", "description must be"},
		{"---\nname: 12\ndescription: d\n---\n", "name must be"},
		{head + "kind: feedback\n---\n", "unknown kind"},
		{head + "scope: acme\n---\n", "scope must be"},
		{head + "scope: project:Acme\n---\n", "scope must be"},
		{head + "sources: linear:ENG-1\n---\n", "sources must be"},
		{head + "sources: [linear:ENG-1, 3]\n---\n", "sources must be"},
		{head + "verified: yesterday\n---\n", "verified must be"},
		{head + "verified: 2026-10-27 10:00:00\n---\n", "verified must be"},
	}
	for _, tt := range tests {
		_, err := Parse([]byte(tt.text))
		if err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("Parse(%q) error = %v, want %q", tt.text, err, tt.err)
		}
	}
}

func TestLoadReportsPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.md")
	if err := os.WriteFile(path, []byte("no frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil || err.Error() != path+": missing frontmatter" {
		t.Errorf("Load() error = %v", err)
	}
}
