package dashboard

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	resolve := func(name string) (string, bool) {
		if name == "deploy-runbook" {
			return "projects/acme/deploy-runbook.md", true
		}
		return "", false
	}
	got, err := renderMarkdown("See [[deploy-runbook|the runbook]] and [[gone]].\n\n<script>alert(1)</script>\n", resolve)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<a href="/notes/projects/acme/deploy-runbook.md">the runbook</a>`, "and gone."} {
		if !strings.Contains(string(got), want) {
			t.Errorf("render lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(string(got), "<script>") {
		t.Errorf("raw html got through:\n%s", got)
	}
}
