package hook

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func fileInput(session, path string) Input {
	in := Input{SessionID: session, ToolName: "Edit"}
	in.ToolInput.FilePath = path
	return in
}

func hookContext(t *testing.T, out string) string {
	t.Helper()
	if out == "" {
		return ""
	}
	var v struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("hook output %q: %v", out, err)
	}
	return v.HookSpecificOutput.AdditionalContext
}

func TestFileNotesShowsTheNotesThatCiteAFileOnce(t *testing.T) {
	env, repo := newEnv(t)
	note := func(name, body string) {
		write(t, filepath.Join(env.Brain, "projects/acme", name+".md"), "---\nname: "+name+"\ndescription: about "+name+"\nkind: reference\nscope: project:acme\n---\n"+body)
	}
	note("beanie-save", "`save()` in `app/email_domain/models.py` drops fields it does not know.\n")
	note("email-domain", "All of `app/email_domain/` follows the same rule.\n")
	note("by-source", "Body.\n")
	write(t, filepath.Join(env.Brain, "projects/acme/by-source.md"), "---\nname: by-source\ndescription: cited as a source\nkind: reference\nscope: project:acme\nsources:\n  - file:"+filepath.Base(repo)+"/app/email_domain/models.py\n---\nBody.\n")
	note("tail", "Look at `email_domain/models.py:42` first.\n")
	note("too-broad", "Everything under `app/` and `models.py` is ours.\n")
	note("elsewhere", "See `src/other.ts`.\n")

	file := filepath.Join(repo, "app", "email_domain", "models.py")
	got := hookContext(t, FileNotes(fileInput("s1", file), env))
	for _, want := range []string{`project "acme" cite app/email_domain/models.py`, "- beanie-save (", "- email-domain (", "- by-source (", "- tail (", ": about beanie-save"} {
		if !strings.Contains(got, want) {
			t.Errorf("context lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "too-broad") || strings.Contains(got, "elsewhere") {
		t.Errorf("context names notes that do not cite the file:\n%s", got)
	}
	if strings.Index(got, "email-domain") < strings.Index(got, "beanie-save") {
		t.Errorf("a folder match came before the notes that name the file:\n%s", got)
	}

	if again := FileNotes(fileInput("s1", file), env); again != "" {
		t.Errorf("the same session saw the notes twice: %s", again)
	}
	if other := hookContext(t, FileNotes(fileInput("s2", file), env)); !strings.Contains(other, "beanie-save") {
		t.Errorf("another session did not see them: %q", other)
	}
	if err := SessionEnd(t.Context(), Input{SessionID: "s1", Cwd: t.TempDir()}, Env{Brain: env.Brain, Home: env.Home}); err != nil {
		t.Fatal(err)
	}
	if after := FileNotes(fileInput("s1", file), env); after == "" {
		t.Error("the notes stayed hidden after the session ended")
	}

	for _, quiet := range []string{filepath.Join(t.TempDir(), "app", "email_domain", "models.py"), filepath.Join(env.Brain, "projects/acme/beanie-save.md"), "relative/path.py", ""} {
		if out := FileNotes(fileInput("s3", quiet), env); out != "" {
			t.Errorf("FileNotes(%q) = %s", quiet, out)
		}
	}
}

func TestFileNotesMarkTheOnesThatContradictTheirSources(t *testing.T) {
	env, repo := newEnv(t)
	write(t, filepath.Join(env.Brain, "projects/acme/beanie-save.md"), "---\nname: beanie-save\ndescription: about saving\nkind: reference\nscope: project:acme\n---\n"+
		"`save()` in `app/models.py` drops fields; see [[gone-note]].\n")
	write(t, filepath.Join(env.Brain, "projects/acme/models.md"), "---\nname: models\ndescription: the models\nkind: reference\nscope: project:acme\n---\n"+
		"The models live in `app/models.py`.\n")
	write(t, filepath.Join(repo, "app", "models.py"), "pass\n")
	in := Input{SessionID: "s1", ToolName: "Read"}
	in.ToolInput.FilePath = filepath.Join(repo, "app", "models.py")

	got := hookContext(t, FileNotes(in, env))
	want := "- beanie-save (~/.wong/projects/acme/beanie-save.md): about saving\n  - it contradicts its sources: [[gone-note]] links to a note that does not exist\n- models"
	if !strings.Contains(got, want) || !strings.Contains(got, "fix it with the wong:maintain-brain skill once you have finished") {
		t.Errorf("context:\n%s", got)
	}
	if strings.Contains(got[strings.Index(got, "- models"):], "  - it ") {
		t.Errorf("a note with nothing found is marked:\n%s", got)
	}
}

func TestFileNotesShowPitfallsFirst(t *testing.T) {
	env, repo := newEnv(t)
	write(t, filepath.Join(env.Brain, "projects/acme/billing.md"), "---\nname: billing\ndescription: how billing works\nkind: reference\nscope: project:acme\n---\nSee `app/payments/billing.py`.\n")
	write(t, filepath.Join(env.Brain, "projects/acme/retries.md"), "---\nname: retries\ndescription: retries double-charge\nkind: pitfall\nscope: project:acme\n---\nRetrying in `app/payments/` charged twice.\n")
	write(t, filepath.Join(repo, "app", "payments", "billing.py"), "pass\n")
	in := Input{SessionID: "s1", ToolName: "Edit"}
	in.ToolInput.FilePath = filepath.Join(repo, "app", "payments", "billing.py")

	got := hookContext(t, FileNotes(in, env))
	if !strings.Contains(got, "or its folder, or apply to it. Read the ones that bear on what you are about to do:\n- retries (~/.wong/projects/acme/retries.md), a pitfall: retries double-charge\n- billing (") {
		t.Errorf("context:\n%s", got)
	}
}

func TestFileNotesShowTheNotesThatApplyToAFile(t *testing.T) {
	env, repo := newEnv(t)
	write(t, filepath.Join(env.Brain, "projects/acme/billing-style.md"), "---\nname: billing-style\ndescription: money is in cents\nkind: preference\nscope: project:acme\n"+
		"applies_to: [app/billing/**, \"*.sql\"]\n---\nAmounts are integers in cents.\n")
	for name, want := range map[string]bool{"app/billing/invoice.py": true, "app/billing/stripe/hooks.py": true, "db/0001_init.sql": true, "app/models.py": false} {
		write(t, filepath.Join(repo, filepath.FromSlash(name)), "x\n")
		in := Input{SessionID: name, ToolName: "Read"}
		in.ToolInput.FilePath = filepath.Join(repo, filepath.FromSlash(name))
		if got := hookContext(t, FileNotes(in, env)); strings.Contains(got, "- billing-style (") != want {
			t.Errorf("%s:\n%s", name, got)
		}
	}
}

func TestFileNotesListSupersededNotesLast(t *testing.T) {
	env, repo := newEnv(t)
	write(t, filepath.Join(env.Brain, "projects/acme/limits-per-ip.md"), "---\nname: limits-per-ip\ndescription: limits per IP\nkind: decision\nscope: project:acme\n---\nLimits in `app/limits.py` go per IP.\n")
	write(t, filepath.Join(env.Brain, "projects/acme/limits-per-key.md"), "---\nname: limits-per-key\ndescription: limits per key\nkind: decision\nscope: project:acme\n"+
		"supersedes: [limits-per-ip]\n---\nLimits in `app/limits.py` go per API key.\n")
	write(t, filepath.Join(repo, "app", "limits.py"), "pass\n")
	in := Input{SessionID: "s1", ToolName: "Read"}
	in.ToolInput.FilePath = filepath.Join(repo, "app", "limits.py")

	got := hookContext(t, FileNotes(in, env))
	if !strings.Contains(got, "\n- limits-per-key (~/.wong/projects/acme/limits-per-key.md): limits per key\n"+
		"- limits-per-ip (~/.wong/projects/acme/limits-per-ip.md), superseded by limits-per-key: limits per IP\n") ||
		!strings.Contains(got, "rely on the one that supersedes it") {
		t.Errorf("context:\n%s", got)
	}
}
