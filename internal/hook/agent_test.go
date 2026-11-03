package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func codexInput(session, cwd, tool, command string) Input {
	in := Input{SessionID: session, Cwd: cwd, ToolName: tool}
	in.ToolInput.Command = command
	return in
}

func TestPatchFiles(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: app/new.py\n+pass\n*** Update File: /abs/old.py\n*** Move to: app/moved.py\n@@\n-a\n+b\n" +
		"*** Delete File: app/gone.py\n*** End Patch\n"
	created := map[string]bool{}
	got := patchFiles(patch, "/repo", created)
	want := []string{"/repo/app/new.py", "/abs/old.py", "/repo/app/moved.py", "/repo/app/gone.py"}
	if !slices.Equal(got, want) {
		t.Errorf("patchFiles = %v, want %v", got, want)
	}
	if !created["/repo/app/new.py"] || len(created) != 1 {
		t.Errorf("created = %v", created)
	}
}

func TestCommandFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "app", "models.py"), "pass\n")
	write(t, filepath.Join(dir, "README.md"), "x\n")
	write(t, filepath.Join(dir, "with space.txt"), "x\n")

	got := commandFiles(`sed -n '1,80p' app/models.py && cat README.md | head; rg -n "def save" app/ --glob '*.py' "with space.txt" missing.py $HOME/x`, dir, dir)
	want := []string{filepath.Join(dir, "app/models.py"), filepath.Join(dir, "README.md"), filepath.Join(dir, "with space.txt")}
	if !slices.Equal(got, want) {
		t.Errorf("commandFiles = %v, want %v", got, want)
	}
	if got := commandFiles("cat ~/README.md", "/elsewhere", dir); !slices.Equal(got, []string{filepath.Join(dir, "README.md")}) {
		t.Errorf("a path from home: %v", got)
	}
	if got := commandFiles("go test ./...", dir, dir); len(got) != 0 {
		t.Errorf("a command that names no file: %v", got)
	}
}

func TestFileNotesForCodex(t *testing.T) {
	env, repo := newEnv(t)
	env.Agent = Codex
	write(t, filepath.Join(env.Brain, "projects/acme/beanie-save.md"), "---\nname: beanie-save\ndescription: about saving\nkind: reference\nscope: project:acme\n---\n"+
		"`save()` in `app/models.py` drops fields it does not know.\n")
	write(t, filepath.Join(env.Brain, "projects/acme/deploy.md"), "---\nname: deploy\ndescription: about deploys\nkind: reference\nscope: project:acme\n---\n"+
		"Deploys run `scripts/deploy.sh`.\n")
	write(t, filepath.Join(repo, "app", "models.py"), "pass\n")
	write(t, filepath.Join(repo, "scripts", "deploy.sh"), "true\n")

	got := hookContext(t, FileNotes(codexInput("s1", repo, "apply_patch", "*** Begin Patch\n*** Update File: app/models.py\n@@\n-pass\n+x\n*** End Patch\n"), env))
	if !strings.Contains(got, `cite app/models.py`) || !strings.Contains(got, "- beanie-save (") || strings.Contains(got, "deploy") {
		t.Errorf("a patch:\n%s", got)
	}
	got = hookContext(t, FileNotes(codexInput("s1", repo, "Bash", "cat scripts/deploy.sh app/models.py"), env))
	if !strings.Contains(got, "cite scripts/deploy.sh") || strings.Contains(got, "beanie-save") {
		t.Errorf("a command, after the patch showed beanie-save:\n%s", got)
	}
	if out := FileNotes(codexInput("s1", repo, "Bash", "ls"), env); out != "" {
		t.Errorf("a command that names no file: %s", out)
	}
}

func TestNoteWrittenForCodex(t *testing.T) {
	env, repo := newEnv(t)
	env.Agent = Codex
	write(t, filepath.Join(env.Brain, "projects/acme/deploy-flow.md"), "---\nname: deploy-flow\ndescription: How Acme ships billing\nkind: reference\nscope: project:acme\ntags: [deploy]\n---\nMerge, then promote.\n")
	write(t, filepath.Join(env.Brain, "projects/acme/billing.md"), "---\nname: billing\ndescription: How Acme ships billing\n---\nShip it.\n")
	write(t, filepath.Join(repo, "app.py"), "pass\n")
	patch := "*** Begin Patch\n*** Add File: " + filepath.Join(env.Brain, "projects/acme/billing.md") + "\n+x\n*** Update File: app.py\n@@\n-pass\n+x\n*** End Patch\n"

	got := NoteWritten(codexInput("s1", repo, "apply_patch", patch), env)
	var out struct {
		Decision           string
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil || out.Decision != "" || out.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Fatalf("NoteWritten = %q, %v", got, err)
	}
	for _, want := range []string{"projects/acme/billing.md is saved", "add `kind`", "deploy-flow.md (How Acme ships billing) seems to cover"} {
		if !strings.Contains(out.HookSpecificOutput.AdditionalContext, want) {
			t.Errorf("context lacks %q:\n%s", want, out.HookSpecificOutput.AdditionalContext)
		}
	}
	if got := NoteWritten(codexInput("s1", repo, "apply_patch", "*** Begin Patch\n*** Update File: app.py\n*** End Patch\n"), env); got != "" {
		t.Errorf("a patch outside the brain: %s", got)
	}
}

func TestSessionStartForCodex(t *testing.T) {
	env, repo := newEnv(t)
	env.Agent = Codex

	got := SessionStart(context.Background(), Input{Cwd: repo}, env)
	for _, want := range []string{
		`wong: the brain's project "acme" (~/.wong/projects/acme) is your memory in this session`,
		"- [A](a.md) — a",
		"- [Use uv](use-uv.md) — uv run, never pip",
		"You have no memory of your own between sessions",
		"`wong note write --to acme`",
		"`scope` (`project:acme` here",
		"with the wong:maintain-brain skill",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("context lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Claude Code") {
		t.Errorf("context speaks of Claude Code:\n%s", got)
	}

	got = SessionStart(context.Background(), Input{Cwd: env.Home}, env)
	for _, want := range []string{"- [Use uv](use-uv.md)", "`wong note write --to me`", "`scope` (`global` here"} {
		if !strings.Contains(got, want) {
			t.Errorf("outside a repo, context lacks %q:\n%s", want, got)
		}
	}
}

func TestIndexIsCutShort(t *testing.T) {
	brain := t.TempDir()
	var lines []string
	for i := range maxIndex + 7 {
		lines = append(lines, "- note "+string(rune('a'+i%26)))
	}
	write(t, filepath.Join(brain, "projects/acme/MEMORY.md"), strings.Join(lines, "\n")+"\n")
	got := index(brain, "projects/acme", "/home", 0)
	if n := strings.Count(got, "\n"); n != maxIndex || !strings.HasSuffix(got, "- and 7 more in "+filepath.Join(brain, "projects/acme/MEMORY.md")) {
		t.Errorf("index has %d lines and ends %q", n+1, got[strings.LastIndex(got, "\n")+1:])
	}
	if got := index(brain, "projects/none", "/home", 0); got != "(no notes yet)" {
		t.Errorf("an empty shelf: %q", got)
	}
}

func cursorContext(t *testing.T, out string) string {
	t.Helper()
	var v map[string]string
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("hook output %q: %v", out, err)
	}
	for k := range v {
		if k != "additional_context" {
			t.Errorf("hook output has %q: %s", k, out)
		}
	}
	return v["additional_context"]
}

func cursorInput(t *testing.T, payload string) Input {
	t.Helper()
	in, err := ReadInput([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestReadInputTakesTheWorkspaceForCursor(t *testing.T) {
	in := cursorInput(t, `{"conversation_id":"c1","session_id":"c1","hook_event_name":"sessionStart","cursor_version":"2026.10.22","workspace_roots":["/repo","/other"],"is_background_agent":false}`)
	if in.Cwd != "/repo" || in.CursorVersion != "2026.10.22" || in.SessionID != "c1" {
		t.Errorf("input = %+v", in)
	}
	in = cursorInput(t, `{"session_id":"c1","cwd":"/repo/app","workspace_roots":["/repo"],"tool_name":"Shell","tool_input":{"command":"cat x","working_directory":"/repo/app"}}`)
	if in.Cwd != "/repo/app" || in.ToolInput.WorkingDirectory != "/repo/app" || in.ToolInput.Command != "cat x" {
		t.Errorf("input = %+v", in)
	}
	if _, err := ReadInput([]byte("not json")); err == nil {
		t.Error("ReadInput took what is not JSON")
	}
}

func TestSessionStartForCursor(t *testing.T) {
	env, repo := newEnv(t)
	env.Agent = Cursor

	got := cursorContext(t, SessionStart(context.Background(), cursorInput(t, `{"session_id":"c1","cursor_version":"2026.10.22","workspace_roots":["`+repo+`"]}`), env))
	for _, want := range []string{
		`wong: the brain's project "acme" (~/.wong/projects/acme) is your memory in this session`,
		"- [A](a.md) — a",
		"- [Use uv](use-uv.md) — uv run, never pip",
		"`wong note write --to acme`",
		"with the maintain-brain skill",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("context lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "wong:maintain-brain") || strings.Contains(got, "Claude Code") || strings.Contains(got, indexMark) {
		t.Errorf("context:\n%s", got)
	}

	var lines []string
	for i := range maxIndex {
		lines = append(lines, fmt.Sprintf("- [Note %03d](note-%03d.md) — %s", i, i, strings.Repeat("ü", 40)))
	}
	write(t, filepath.Join(env.Brain, "projects/acme/MEMORY.md"), strings.Join(lines, "\n")+"\n")
	got = cursorContext(t, SessionStart(context.Background(), Input{Cwd: repo}, env))
	if len(got) > maxContext || !strings.Contains(got, "- [Note 000](note-000.md)") || !strings.Contains(got, " more in ~/.wong/projects/acme/MEMORY.md\n") ||
		!strings.Contains(got, "1 finding says") {
		t.Errorf("a long index gives %d bytes:\n%s", len(got), got)
	}

	got = cursorContext(t, SessionStart(context.Background(), Input{Cwd: env.Home}, env))
	if !strings.Contains(got, "- [Use uv](use-uv.md)") || !strings.Contains(got, "`wong note write --to me`") {
		t.Errorf("outside a repo:\n%s", got)
	}
}

func TestFileNotesForCursor(t *testing.T) {
	env, repo := newEnv(t)
	env.Agent = Cursor
	write(t, filepath.Join(env.Brain, "projects/acme/beanie-save.md"), "---\nname: beanie-save\ndescription: about saving\nkind: reference\nscope: project:acme\n---\n"+
		"`save()` in `app/models.py` drops fields it does not know.\n")
	write(t, filepath.Join(repo, "app", "models.py"), "pass\n")

	read := `{"session_id":"c1","cursor_version":"2026.10.22","workspace_roots":["` + repo + `"],"cwd":"` + repo + `","tool_name":"Read","tool_input":{"file_path":"` + filepath.Join(repo, "app", "models.py") + `"}}`
	if got := cursorContext(t, FileNotes(cursorInput(t, read), env)); !strings.Contains(got, "cite app/models.py") || !strings.Contains(got, "- beanie-save (") {
		t.Errorf("a read:\n%s", got)
	}
	shell := `{"session_id":"c2","cwd":"` + repo + `","tool_name":"Shell","tool_input":{"command":"sed -n 1,20p models.py","working_directory":"app"}}`
	if got := cursorContext(t, FileNotes(cursorInput(t, shell), env)); !strings.Contains(got, "cite app/models.py") {
		t.Errorf("a command in a folder of the repo:\n%s", got)
	}
	if out := FileNotes(cursorInput(t, read), env); out != "{}" {
		t.Errorf("a note already shown: %q", out)
	}
}

func TestNoteWrittenForCursor(t *testing.T) {
	env, repo := newEnv(t)
	env.Agent = Cursor
	run(t, env.Home, "init", "-q", env.Brain)
	write(t, filepath.Join(env.Brain, "projects/acme/deploy-flow.md"), "---\nname: deploy-flow\ndescription: How Acme ships billing\nkind: reference\nscope: project:acme\ntags: [deploy]\n---\nMerge, then promote.\n")
	note := filepath.Join(env.Brain, "projects/acme/billing.md")
	write(t, note, "---\nname: billing\ndescription: How Acme ships billing\nkind: reference\nscope: project:acme\ntags: [deploy]\n---\nShip it.\n")
	payload := `{"session_id":"c1","cwd":"` + repo + `","tool_name":"Write","tool_input":{"file_path":"` + note + `","content":"x"}}`

	got := cursorContext(t, NoteWritten(cursorInput(t, payload), env))
	if !strings.Contains(got, "deploy-flow.md (How Acme ships billing) seems to cover") {
		t.Errorf("a new note:\n%s", got)
	}
	run(t, env.Brain, "add", ".")
	run(t, env.Brain, "commit", "-q", "-m", "notes")
	if out := NoteWritten(cursorInput(t, payload), env); out != "{}" {
		t.Errorf("an edit of a note git tracks: %s", out)
	}
	if out := NoteWritten(cursorInput(t, `{"session_id":"c1","tool_name":"Write","tool_input":{"file_path":"`+filepath.Join(repo, "app.py")+`"}}`), env); out != "{}" {
		t.Errorf("a file outside the brain: %s", out)
	}
}

func TestClipCutsAtALine(t *testing.T) {
	text := strings.Repeat("línea con acentos\n", 100)
	got := clip(text, 300)
	if len(got) > 300 || !utf8.ValidString(got) || !strings.HasSuffix(got, "to fit what the agent takes from a hook)") || !strings.Contains(got, "acentos\n(wong cut") {
		t.Errorf("clip = %q", got)
	}
	if got := clip("short", 300); got != "short" {
		t.Errorf("clip of a short text = %q", got)
	}
}
