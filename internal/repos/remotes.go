package repos

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// RemotesFile is the file at the root of the brain that tells the server which project each GitHub repo belongs to.
const RemotesFile = "remotes.yaml"

var githubRE = regexp.MustCompile(`^(?:(?:https?|ssh|git)://(?:[^@/]+@)?github\.com(?::\d+)?/|[^@/:]+@github\.com:)([^/]+)/([^/]+?)(?:\.git)?/?$`)

// Remote is the project of a GitHub repo and the name of its directory, which notes use to cite its files.
type Remote struct {
	Project string `yaml:"project"`
	Dir     string `yaml:"dir"`
}

// Remotes maps a GitHub repo, as owner/name, to its project.
type Remotes map[string]Remote

func ParseRemotes(data []byte) (Remotes, error) {
	r := Remotes{}
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", RemotesFile, err)
	}
	if r == nil {
		r = Remotes{}
	}
	return r, nil
}

// GitHubRepo returns the owner/name of a GitHub remote URL.
func GitHubRepo(url string) (string, bool) {
	m := githubRE.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", false
	}
	return m[1] + "/" + m[2], true
}

// WriteRemotes adds the GitHub origin of each checkout in the registry to the brain's remotes.yaml, keeping the repos other machines added.
func WriteRemotes(brain string, reg Registry) error {
	path := filepath.Join(brain, RemotesFile)
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	r, err := ParseRemotes(existing)
	if err != nil {
		return err
	}
	for _, project := range slices.Sorted(maps.Keys(reg)) {
		for _, dir := range reg[project] {
			out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
			if err != nil {
				continue
			}
			if repo, ok := GitHubRepo(string(out)); ok {
				r[repo] = Remote{Project: project, Dir: filepath.Base(dir)}
			}
		}
	}
	if len(r) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(map[string]Remote(r)); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if bytes.Equal(existing, buf.Bytes()) {
		return nil
	}
	if err := os.MkdirAll(brain, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
