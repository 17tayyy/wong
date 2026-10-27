package check

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/linear"
	"github.com/17tayyy/wong/internal/note"
)

// Note is one note of the brain as a server holds it: its path, its content and when its file was written.
type Note struct {
	Path    string
	Data    []byte
	Written time.Time
}

// IssueClosed applies the rules for a closed issue to a note that may cite it.
func IssueClosed(n Note, issue linear.Issue) (Finding, bool, error) {
	l, err := loadNote(n)
	if err != nil || l.kind == note.Preference || !slices.Contains(l.refs.issues, issue.Identifier) {
		return Finding{}, false, err
	}
	f, found := l.issueFinding(issue)
	return f, found, nil
}

// FileRemoved applies the rules for a file removed from a repo (by its directory name) to a note that may cite it.
func FileRemoved(n Note, repo, path, commit string, when time.Time) (Finding, bool, error) {
	l, err := loadNote(n)
	if err != nil {
		return Finding{}, false, err
	}
	for _, ref := range l.refs.files {
		if filepath.IsAbs(ref) || !sameFile(path, strings.TrimPrefix(ref, repo+"/")) {
			continue
		}
		if !when.After(l.asOf) {
			return Finding{}, false, nil
		}
		return Finding{l.rel, Stale, ref, deletedMessage(commit, when, l.asOf)}, true, nil
	}
	return Finding{}, false, nil
}

func loadNote(n Note) (loaded, error) {
	parsed, err := note.Parse(n.Data)
	if err != nil {
		return loaded{}, err
	}
	return newLoaded(n.Path, parsed, n.Written), nil
}
