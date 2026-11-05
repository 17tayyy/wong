package remote

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/note"
)

const (
	checkName   = "wong"
	checksKept  = 30 * 24 * time.Hour
	maxCheckRow = 200
)

// pullRequest is a pull request a sealed brain keeps for its machines to compare with their copy of the notes: the
// check that waits for the answer, and the files the pull request changes.
type pullRequest struct {
	Check  int64    `json:"check"`
	Number int      `json:"number"`
	Files  []prFile `json:"files"`
}

// CheckFile is what a pull request does to a file the brain's notes reach: how many notes cite it when it is removed
// or moved, and how many pitfalls are noted on it when it changes.
type CheckFile struct {
	Path     string `json:"path"`
	To       string `json:"to,omitempty"`
	Change   string `json:"change"`
	Notes    int    `json:"notes,omitempty"`
	Pitfalls int    `json:"pitfalls,omitempty"`
}

// CheckAnswer is what comparing a pull request with the notes found, in counts alone: no note is named, since the
// check shows on GitHub to everyone who can see the pull request. Linked is false when the repo is in no project.
type CheckAnswer struct {
	Check    int64       `json:"check"`
	Linked   bool        `json:"linked"`
	Notes    int         `json:"notes"`
	Pitfalls int         `json:"pitfalls"`
	Files    []CheckFile `json:"files"`
}

// prReport compares the files a pull request changes in a repo with the notes of the repo's project.
func prReport(notes []check.Note, repo string, files []prFile) CheckAnswer {
	now := time.Now().UTC()
	var pitfalls []check.Note
	for _, n := range notes {
		if parsed, err := note.Parse(n.Data); err == nil && parsed.Kind == note.Pitfall {
			pitfalls = append(pitfalls, n)
		}
	}
	answer := CheckAnswer{Linked: true}
	cite, warn := map[string]bool{}, map[string]bool{}
	for _, f := range files {
		cf := CheckFile{Path: f.Path, Change: "changed"}
		switch f.Status {
		case "removed":
			cf.Change = "removed"
		case "renamed":
			cf.Change, cf.Path, cf.To = "moved", f.Previous, f.Path
		}
		if cf.Change == "changed" {
			for _, n := range pitfalls {
				if note.Reach(n.Data, cf.Path, repo, "", "") > 0 {
					cf.Pitfalls++
					warn[n.Path] = true
				}
			}
		} else {
			for _, n := range notes {
				if _, found, err := check.FileRemoved(n, repo, cf.Path, "", now); err == nil && found {
					cf.Notes++
					cite[n.Path] = true
				}
			}
		}
		if cf.Notes+cf.Pitfalls > 0 {
			answer.Files = append(answer.Files, cf)
		}
	}
	answer.Notes, answer.Pitfalls = len(cite), len(warn)
	return answer
}

// waitingRun is the check a sealed brain's pull request shows until one of its machines answers.
func waitingRun(sha string) checkRun {
	return checkRun{Name: checkName, HeadSHA: sha, Status: "queued", Output: &checkOutput{
		Title: "Waiting for one of your machines",
		Summary: "The brain is end-to-end encrypted, so the wong server cannot compare this pull request with its notes. " +
			"The next of your machines to sync with it does, and the result shows here.",
	}}
}

// checkResult is the check that says what comparing a pull request with the notes found.
func checkResult(a CheckAnswer) checkRun {
	run := checkRun{Status: "completed", Conclusion: "success"}
	if !a.Linked {
		run.Conclusion = "neutral"
		run.Output = &checkOutput{
			Title: "This repository is in no project of the brain",
			Summary: "wong compares a pull request with the notes of the project its repository belongs to. Link the " +
				"repository to a project with `wong link`, on a machine where it is cloned, and its next pull requests are checked.",
		}
		return run
	}
	if a.Notes == 0 && a.Pitfalls == 0 {
		run.Output = &checkOutput{
			Title:   "Nothing in the brain to read first",
			Summary: "No note cites a file this pull request removes or moves, and no pitfall is noted on a file it changes.",
		}
		return run
	}
	run.Conclusion = "neutral"
	var parts []string
	if a.Notes > 0 {
		parts = append(parts, plural(a.Notes, "note cites", "notes cite")+" files this pull request removes or moves")
	}
	if a.Pitfalls > 0 {
		parts = append(parts, plural(a.Pitfalls, "pitfall is", "pitfalls are")+" noted on files it changes")
	}
	title := strings.Join(parts, ", and ")
	var b strings.Builder
	b.WriteString("| File | Change | Notes that cite it | Pitfalls noted on it |\n|---|---|---|---|\n")
	for i, f := range a.Files {
		if i == maxCheckRow {
			fmt.Fprintf(&b, "\nAnd %d more files.\n", len(a.Files)-maxCheckRow)
			break
		}
		file := "`" + cell(f.Path) + "`"
		if f.To != "" {
			file += " → `" + cell(f.To) + "`"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", file, f.Change, count(f.Notes), count(f.Pitfalls))
	}
	b.WriteString("\nWhich notes they are stays in the brain. Open these files with your agent and wong shows it the notes; " +
		"the ones that cite a removed or moved file come back as tasks once this is merged.")
	run.Output = &checkOutput{Title: strings.ToUpper(title[:1]) + title[1:], Summary: b.String()}
	return run
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func count(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprint(n)
}

func cell(s string) string {
	return strings.NewReplacer("|", `\|`, "`", "'", "\n", " ").Replace(s)
}

// takePR checks a pull request of a repo an installation of the GitHub App picked. For a brain in the clear the server
// compares it with the notes at once; for a sealed one it posts a check that waits, and keeps the pull request for the
// next machine to sync, which answers with what it found.
func takePR(ctx context.Context, app *GitHubApp, store *Store, installation int64, repo string, number int, sha string) error {
	sealed, err := store.Sealed()
	if err != nil {
		return err
	}
	if sealed {
		run, err := app.CreateCheck(ctx, installation, repo, waitingRun(sha))
		if err != nil {
			return err
		}
		files, err := app.PRFiles(ctx, installation, repo, number)
		if err == nil {
			var id int64
			if id, err = store.keepCheck(installation, repo, run); err == nil {
				err = store.keepEvent(event{PR: &pullRequest{Check: id, Number: number, Files: files}}, fmt.Sprintf("github %s#%d", repo, number), repo)
			}
		}
		if err != nil {
			failed := checkRun{Status: "completed", Conclusion: "neutral", Output: &checkOutput{
				Title: "wong could not read this pull request", Summary: "Push to it again and wong tries once more."}}
			return errors.Join(err, app.UpdateCheck(ctx, installation, repo, run, failed))
		}
		return nil
	}
	files, err := app.PRFiles(ctx, installation, repo, number)
	if err != nil {
		return err
	}
	remotes, err := store.remotes()
	if err != nil {
		return err
	}
	var answer CheckAnswer
	if r, ok := remotes.Lookup(repo); ok {
		notes, err := store.notes(r.Project)
		if err != nil {
			return err
		}
		answer = prReport(notes, r.Dir, files)
	}
	run := checkResult(answer)
	run.Name, run.HeadSHA = checkName, sha
	_, err = app.CreateCheck(ctx, installation, repo, run)
	return err
}

// answerCheck posts what a machine found for a pull request on the check that waited for it. A check already
// answered, by another machine, or one the server no longer keeps, is left alone.
func answerCheck(ctx context.Context, app *GitHubApp, store *Store, a CheckAnswer) error {
	installation, repo, run, err := store.waitingCheck(a.Check)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := app.UpdateCheck(ctx, installation, repo, run, checkResult(a)); err != nil {
		return err
	}
	_, err = store.db.Exec(`UPDATE checks SET answered_at = ? WHERE id = ?`, utcNow(), a.Check)
	return err
}

// keepCheck remembers a check that waits for a machine's answer, and forgets the ones older than checksKept.
func (s *Store) keepCheck(installation int64, repo string, run int64) (int64, error) {
	now := time.Now().UTC()
	if _, err := s.db.Exec(`DELETE FROM checks WHERE created_at < ?`, now.Add(-checksKept).Format(time.RFC3339)); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO checks (installation, repo, run, created_at) VALUES (?, ?, ?, ?)`, installation, repo, run, now.Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) waitingCheck(id int64) (installation int64, repo string, run int64, err error) {
	err = s.db.QueryRow(`SELECT installation, repo, run FROM checks WHERE id = ? AND answered_at = ''`, id).Scan(&installation, &repo, &run)
	return
}
