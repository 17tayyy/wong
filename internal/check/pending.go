package check

import (
	"fmt"
	"time"
)

// released finds the work a note leaves for a release that came after the note was written or verified.
func (l loaded) released(repo, tag, by string, when time.Time) (Finding, bool) {
	if l.pending != "release" || !when.After(l.asOf) {
		return Finding{}, false
	}
	return Finding{l.rel, Stale, "release", fmt.Sprintf("%s released %s on %s%s: tell the user what this note leaves for the release, "+
		"do your part, then update the note, dropping pending once nothing is left", repo, tag, day(when), pushedBy(by))}, true
}

// pushed finds the work a note leaves for new commits on the main branch since the note was written or verified.
func (l loaded) pushed(repo, branch, sha, by string, when time.Time) (Finding, bool) {
	if l.pending != "main" || !when.After(l.asOf) {
		return Finding{}, false
	}
	return Finding{l.rel, Stale, "main", fmt.Sprintf("%s got new commits on %s up to %s on %s%s: tell the user what this note leaves for "+
		"them, do your part, then update the note, dropping pending once nothing is left", repo, branch, sha, day(when), pushedBy(by))}, true
}

// Deploy reports whether a finding is work a note leaves for a release or a push to the main branch that has happened.
func (f Finding) Deploy() bool {
	return f.Severity == Stale && (f.Ref == "release" || f.Ref == "main")
}

func pushedBy(by string) string {
	if by == "" {
		return ""
	}
	return ", pushed by " + by
}

// Released applies the rule of a release, a v* tag pushed to a repo, to a note that may leave work for it.
func Released(n Note, repo, tag, by string, when time.Time) (Finding, bool, error) {
	l, err := loadNote(n)
	if err != nil {
		return Finding{}, false, err
	}
	f, found := l.released(repo, tag, by, when)
	return f, found, nil
}

// Pushed applies the rule of new commits on a repo's main branch to a note that may leave work for them.
func Pushed(n Note, repo, branch, sha, by string, when time.Time) (Finding, bool, error) {
	l, err := loadNote(n)
	if err != nil {
		return Finding{}, false, err
	}
	f, found := l.pushed(repo, branch, sha, by, when)
	return f, found, nil
}
