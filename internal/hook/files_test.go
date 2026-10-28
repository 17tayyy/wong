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

func TestReaches(t *testing.T) {
	for _, c := range []struct {
		ref  string
		want int
	}{
		{"app/x/models.py", 2}, {"./app/x/models.py:12", 2}, {"api/app/x/models.py", 2}, {"x/models.py", 2},
		{"app/x", 1}, {"app/x/", 1}, {"api/app", 1}, {"/home/u/api/app/x", 1}, {"~/api/app/x/models.py", 2},
		{"app", 0}, {"app/", 0}, {"models.py", 0}, {"app/y", 0}, {"app/*.py", 0}, {"/home/u/other/app/x", 0}, {"/home/u/api", 0},
	} {
		if got := reaches(c.ref, "app/x/models.py", "api", "/home/u/api", "/home/u"); got != c.want {
			t.Errorf("reaches(%q) = %d, want %d", c.ref, got, c.want)
		}
	}
	for ref, want := range map[string]int{"ai_core/": 1, "ai_core": 0, "src/": 0} {
		if got := reaches(ref, "ai_core/prompts.py", "api", "/home/u/api", "/home/u"); got != want {
			t.Errorf("reaches(%q) on a top-level folder = %d, want %d", ref, got, want)
		}
	}
}
