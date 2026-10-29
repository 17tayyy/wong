package hook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/17tayyy/wong/internal/linear"
	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/repos"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T) (Env, string) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t",
	} {
		t.Setenv(k, v)
	}
	home := t.TempDir()
	repo := filepath.Join(home, "acme", "acme-api")
	run(t, home, "init", "-q", repo)
	brain := filepath.Join(home, ".wong")
	write(t, filepath.Join(brain, "me", "MEMORY.md"), "- [Use uv](use-uv.md) — uv run, never pip\n")
	write(t, filepath.Join(brain, "projects", "acme", "MEMORY.md"), "- [A](a.md) — a\n")
	write(t, filepath.Join(brain, "projects", "acme", "a.md"), "---\nname: a\ndescription: d\n---\nSee [[gone]].\n")
	return Env{Brain: brain, Home: home, Repos: repos.Registry{"acme": {repo}}}, repo
}

func TestSessionStartInALinkedRepo(t *testing.T) {
	env, repo := newEnv(t)

	got := SessionStart(context.Background(), Input{Cwd: filepath.Join(repo)}, env)

	for _, want := range []string{
		`wong: this session's memory is the brain's project "acme" (~/.wong/projects/acme).`,
		"the notes are in ~/.wong/me",
		"- [Use uv](use-uv.md) — uv run, never pip",
		"1 finding says notes of this project's memory contradict their sources",
		"wong:maintain-brain",
		"- projects/acme/a.md: [[gone]] links to a note that does not exist",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("context lacks %q:\n%s", want, got)
		}
	}
}

func TestSessionStartInAnUnlinkedRepo(t *testing.T) {
	env, _ := newEnv(t)
	other := filepath.Join(env.Home, "other")
	run(t, env.Home, "init", "-q", other)

	got := SessionStart(context.Background(), Input{Cwd: other}, env)

	if !strings.Contains(got, "this repo is not linked to the brain") || !strings.Contains(got, "(in the brain: acme)") {
		t.Errorf("context = %q", got)
	}
}

func TestSessionStartOutsideARepoAddsNothing(t *testing.T) {
	env, _ := newEnv(t)

	if got := SessionStart(context.Background(), Input{Cwd: env.Home}, env); got != "" {
		t.Errorf("context = %q", got)
	}
}

func TestSessionEndCommitsTheBrain(t *testing.T) {
	env, repo := newEnv(t)
	run(t, env.Brain, "init", "-q")
	run(t, env.Brain, "add", "-A")
	run(t, env.Brain, "commit", "-q", "-m", "start")

	if err := SessionEnd(context.Background(), Input{SessionID: "0123456789ab", Cwd: repo}, env); err != nil {
		t.Fatal(err)
	}
	if got := run(t, env.Brain, "log", "--format=%s"); got != "start" {
		t.Errorf("a session that changed nothing committed: %q", got)
	}

	write(t, filepath.Join(env.Brain, "projects", "acme", "b.md"), "---\nname: b\ndescription: d\n---\nNew.\n")
	if err := SessionEnd(context.Background(), Input{SessionID: "0123456789ab", Cwd: repo}, env); err != nil {
		t.Fatal(err)
	}
	if got := run(t, env.Brain, "log", "-1", "--format=%s"); got != "session 01234567 in acme" {
		t.Errorf("last commit = %q", got)
	}
}

func TestSessionEndRecordsTheGitHubRepos(t *testing.T) {
	env, repo := newEnv(t)
	run(t, repo, "remote", "add", "origin", "git@github.com:acme/acme-api.git")

	if err := SessionEnd(context.Background(), Input{SessionID: "s", Cwd: repo}, env); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(env.Brain, repos.RemotesFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := "acme/acme-api:\n  project: acme\n  dir: acme-api\n"; string(data) != want {
		t.Errorf("remotes.yaml = %q, want %q", data, want)
	}
}

func TestSessionEndIgnoresABrainWithoutGit(t *testing.T) {
	env, repo := newEnv(t)

	if err := SessionEnd(context.Background(), Input{SessionID: "s", Cwd: repo}, env); err != nil {
		t.Errorf("SessionEnd() = %v", err)
	}
}

func TestSessionsSyncThroughTheServer(t *testing.T) {
	env, repo := newEnv(t)
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv := httptest.NewServer(remote.Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), remote.Webhooks{}))
	defer srv.Close()
	client := func(name string) *remote.Client {
		token, err := store.NewToken(name)
		if err != nil {
			t.Fatal(err)
		}
		return remote.NewClient(srv.URL, token, 5*time.Second)
	}
	env.Remote = client("desktop")
	if err := SessionEnd(context.Background(), Input{SessionID: "s1", Cwd: repo}, env); err != nil {
		t.Fatal(err)
	}

	laptop := env
	laptop.Brain = filepath.Join(t.TempDir(), "brain")
	laptop.Remote = client("laptop")
	got := SessionStart(context.Background(), Input{Cwd: repo}, laptop)

	if data, err := os.ReadFile(filepath.Join(laptop.Brain, "projects", "acme", "a.md")); err != nil || !strings.Contains(string(data), "See [[gone]].") {
		t.Errorf("the laptop did not get the note: %v", err)
	}
	if !strings.Contains(got, `the brain's project "acme"`) {
		t.Errorf("context = %q", got)
	}

	write(t, filepath.Join(env.Brain, "me", "MEMORY.md"), "- desktop\n")
	write(t, filepath.Join(laptop.Brain, "me", "MEMORY.md"), "- laptop\n")
	if err := SessionEnd(context.Background(), Input{SessionID: "s2", Cwd: repo}, env); err != nil {
		t.Fatal(err)
	}
	if err := SessionEnd(context.Background(), Input{SessionID: "s3", Cwd: repo}, laptop); err != nil {
		t.Fatal(err)
	}
	if got := SessionStart(context.Background(), Input{Cwd: repo}, laptop); !strings.Contains(got, "1 notes of the brain changed both here and on another machine") {
		t.Errorf("context = %q", got)
	}
}

type closedIssues map[string]linear.Issue

func (c closedIssues) TeamKeys(context.Context) ([]string, error) { return []string{"ENG"}, nil }

func (c closedIssues) Issues(context.Context, []string) (map[string]linear.Issue, error) {
	return c, nil
}

func TestSessionStartShowsTheServersTasks(t *testing.T) {
	env, repo := newEnv(t)
	write(t, filepath.Join(env.Brain, "projects", "acme", "b.md"), "---\nname: b\ndescription: d\n---\nENG-15 en pausa.\n")
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(env.Brain, "projects", "acme", "b.md"), old, old); err != nil {
		t.Fatal(err)
	}
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv := httptest.NewServer(remote.Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), remote.Webhooks{LinearSecret: "secret"}))
	defer srv.Close()
	token, err := store.NewToken("desktop")
	if err != nil {
		t.Fatal(err)
	}
	env.Remote = remote.NewClient(srv.URL, token, 5*time.Second)
	if err := SessionEnd(context.Background(), Input{SessionID: "s", Cwd: repo}, env); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"action": "update", "type": "Issue", "createdAt": "2026-10-20T10:00:00Z", "webhookTimestamp": %d,
		"data": {"identifier": "ENG-15", "completedAt": "2026-10-20T10:00:00Z", "state": {"name": "Done", "type": "completed"}}}`, time.Now().UnixMilli())
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte(body))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/hooks/linear", bytes.NewReader([]byte(body)))
	req.Header.Set("Linear-Signature", hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("linear webhook: %v, %v", resp, err)
	}
	resp.Body.Close()
	task := `- projects/acme/b.md: ENG-15 Done since 2026-10-20, but the note says "en pausa"`

	got := SessionStart(context.Background(), Input{Cwd: repo}, env)
	for _, want := range []string{"2 findings say", task, "`wong tasks --project acme`"} {
		if !strings.Contains(got, want) {
			t.Errorf("context lacks %q:\n%s", want, got)
		}
	}

	env.Tracker = closedIssues{"ENG-15": {Identifier: "ENG-15", State: "Done", Type: "completed", ClosedAt: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)}}
	got = SessionStart(context.Background(), Input{Cwd: repo}, env)
	if !strings.Contains(got, "2 findings say") || strings.Count(got, task) != 1 {
		t.Errorf("a task that check also found is listed twice:\n%s", got)
	}
}

func TestSessionStartNamesTheNotesToReview(t *testing.T) {
	env, repo := newEnv(t)
	for i := range 17 {
		name := fmt.Sprintf("n%02d", i)
		write(t, filepath.Join(env.Brain, "projects", "acme", name+".md"), "---\nname: "+name+"\ndescription: d\n---\nSee `app/missing_"+name+".py`.\n")
	}
	got := SessionStart(t.Context(), Input{Cwd: repo}, env)
	if !strings.Contains(got, "these 17 notes of this project may be outdated: n00.md, n01.md, ") || !strings.Contains(got, "n14.md and 2 more.") ||
		strings.Contains(got, "n15.md") || !strings.Contains(got, "If one bears on your task") || !strings.Contains(got, "maintain up to 3 of the others") {
		t.Errorf("session start:\n%s", got)
	}
}

func TestSessionStartPutsWorkLeftForADeployFirst(t *testing.T) {
	env, repo := newEnv(t)
	note := filepath.Join(env.Brain, "projects", "acme", "prod-steps.md")
	write(t, note, "---\nname: prod-steps\ndescription: d\npending: release\n---\nRun the migration in prod.\n")
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(note, old, old); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	run(t, repo, "tag", "-a", "v1.0.0", "-m", "v1.0.0")
	got := SessionStart(t.Context(), Input{Cwd: repo}, env)
	deploy := strings.Index(got, "leave work for a deploy that has happened")
	if deploy < 0 || !strings.Contains(got, "- projects/acme/prod-steps.md: acme-api released v1.0.0 on ") ||
		strings.Count(got, "prod-steps") != 1 || deploy > strings.Index(got, "1 finding says") {
		t.Errorf("session start:\n%s", got)
	}
}

func TestNoteWrittenAsksForWhatWongNeeds(t *testing.T) {
	env, repo := newEnv(t)
	write(t, filepath.Join(repo, "app", "billing.py"), "pass\n")
	run(t, repo, "add", "app/billing.py")
	write(t, filepath.Join(env.Brain, "projects", "acme", "deploy-flow.md"), "---\nname: deploy-flow\ndescription: How Acme ships billing\nkind: reference\nscope: project:acme\ntags: [deploy]\n---\nMerge, then promote.\n")
	written := func(tool, rel, content string) string {
		t.Helper()
		p := filepath.Join(env.Brain, rel)
		write(t, p, content)
		in := Input{ToolName: tool}
		in.ToolInput.FilePath = p
		return NoteWritten(in, env)
	}

	write(t, filepath.Join(env.Brain, ".wong", "linear-teams"), "ENG\n")
	got := written("Write", "projects/acme/billing.md", "---\nname: billing\ndescription: How Acme ships billing\nmetadata:\n  type: feedback\n"+
		"pending: later\n---\nSee `app/billing.py`, ENG-15 and [[nope]], under AGPL-3.0.\n")
	var out struct{ Decision, Reason string }
	if err := json.Unmarshal([]byte(got), &out); err != nil || out.Decision != "block" {
		t.Fatalf("NoteWritten = %q, %v", got, err)
	}
	if strings.Contains(out.Reason, "AGPL") {
		t.Errorf("a license version taken for an issue:\n%s", out.Reason)
	}
	for _, want := range []string{"projects/acme/billing.md is saved", "add `kind`", "metadata.type says preference", "set `scope: project:acme`",
		`unknown pending "later"`, "reusing those in use: deploy", "[[nope]] reaches no note", "projects/acme/deploy-flow.md (How Acme ships billing) seems to cover",
		"`sources`, so wong notices when it changes: linear:ENG-15, file:acme-api/app/billing.py"} {
		if !strings.Contains(out.Reason, want) {
			t.Errorf("reason lacks %q:\n%s", want, out.Reason)
		}
	}

	if got := written("Edit", "projects/acme/billing.md", "---\nname: billing\ndescription: Billing runs on Stripe\nkind: decision\nscope: project:acme\n"+
		"tags: [deploy, Deployment]\nsources:\n  - linear:ENG-15\n  - file:acme-api/app/billing.py\n---\nSee `app/billing.py` and ENG-15.\n"); !strings.Contains(got, `\"deployment\" is a spelling of \"deploy\"`) {
		t.Errorf("a tag that is a spelling of another: %s", got)
	}
	if got := written("Edit", "projects/acme/billing.md", "---\nname: billing\ndescription: Billing runs on Stripe\nkind: decision\nscope: project:acme\n"+
		"tags: [deploy, billing]\nsources:\n  - linear:ENG-15\n  - file:acme-api/app/billing.py\n---\nSee `app/billing.py`, ENG-15 and [[deploy-flow]].\n"); got != "" {
		t.Errorf("a note in wong's format: %s", got)
	}
	if got := written("Write", "me/no-frontmatter.md", "Just text.\n"); !strings.Contains(got, "frontmatter does not parse") {
		t.Errorf("a note without frontmatter: %s", got)
	}
	for _, rel := range []string{"projects/acme/MEMORY.md", ".wong/notes.md", "projects/acme/sub/x.md"} {
		if got := written("Write", rel, "x\n"); got != "" {
			t.Errorf("%s: %s", rel, got)
		}
	}
	in := Input{ToolName: "Write"}
	in.ToolInput.FilePath = filepath.Join(repo, "README.md")
	if got := NoteWritten(in, env); got != "" {
		t.Errorf("a file outside the brain: %s", got)
	}
}
