package check

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type repo struct {
	dir     string
	tracked []string
	deleted map[string]deletion
	remote  map[string]bool
}

type deletion struct {
	commit string
	when   time.Time
}

func openRepo(dir string) (*repo, error) {
	r := &repo{dir: dir, deleted: map[string]deletion{}, remote: map[string]bool{}}
	out, err := git(dir, "", "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	if out != "" {
		r.tracked = strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	}
	out, err = git(dir, "", "log", "--all", "--no-renames", "--diff-filter=D", "--name-only", "--format=%x00%h %ct")
	if err != nil {
		return nil, err
	}
	for _, block := range strings.Split(out, "\x00")[1:] {
		head, paths, _ := strings.Cut(block, "\n")
		commit, unix, _ := strings.Cut(head, " ")
		seconds, err := strconv.ParseInt(unix, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("git log in %s: unexpected line %q", dir, head)
		}
		for _, p := range strings.Split(paths, "\n") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			if _, seen := r.deleted[p]; !seen {
				r.deleted[p] = deletion{commit, time.Unix(seconds, 0)}
			}
		}
	}
	out, err = git(dir, "", "rev-list", "--remotes")
	if err != nil {
		return nil, err
	}
	for _, sha := range strings.Fields(out) {
		r.remote[sha] = true
	}
	return r, nil
}

func (r *repo) rel(path string) (string, bool) {
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(r.dir, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return "", false
		}
		return rel, true
	}
	return strings.TrimPrefix(path, filepath.Base(r.dir)+"/"), true
}

func (r *repo) has(path string) bool {
	rel, ok := r.rel(path)
	if !ok {
		return false
	}
	if slices.ContainsFunc(r.tracked, func(p string) bool { return sameFile(p, rel) }) {
		return true
	}
	_, err := os.Stat(filepath.Join(r.dir, rel))
	return err == nil
}

func (r *repo) deletedAt(path string) (deletion, bool) {
	rel, ok := r.rel(path)
	if !ok {
		return deletion{}, false
	}
	var found deletion
	for p, d := range r.deleted {
		if sameFile(p, rel) && (d.when.After(found.when) || d.when.Equal(found.when) && d.commit > found.commit) {
			found = d
		}
	}
	return found, found.commit != ""
}

// commits maps each of shas that names a commit in this repo to its full hash.
func (r *repo) commits(shas []string) (map[string]string, error) {
	full := map[string]string{}
	if len(shas) == 0 {
		return full, nil
	}
	var in strings.Builder
	for _, s := range shas {
		in.WriteString(s + "^{commit}\n")
	}
	out, err := git(r.dir, in.String(), "cat-file", "--batch-check=%(objectname)")
	if err != nil {
		return nil, err
	}
	for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if i < len(shas) && !strings.Contains(line, " ") {
			full[shas[i]] = line
		}
	}
	return full, nil
}

func sameFile(tracked, rel string) bool {
	return tracked == rel || strings.HasSuffix(tracked, "/"+rel)
}

func git(dir, stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.quotePath=false"}, args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", fmt.Errorf("git %s in %s: %s", args[0], dir, strings.TrimSpace(string(exit.Stderr)))
	}
	return string(out), err
}
