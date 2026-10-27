package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/link"
	"github.com/17tayyy/wong/internal/note"
	"github.com/17tayyy/wong/internal/repos"
)

type linkTarget struct {
	repo, project string
}

func newLinkCmd(home string, brain *string) *cobra.Command {
	var project string
	var all, remove bool
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Make Claude Code keep this repo's memory in the brain",
		Long: "Make Claude Code keep this repo's memory in the brain.\n\n" +
			"Sets autoMemoryDirectory in the repo's .claude/settings.local.json to the project's\n" +
			"folder in the brain, so sessions there read and write the brain instead of\n" +
			"~/.claude/projects. The project comes from --project or from the repos that\n" +
			"wong import registered. Restart open sessions so they pick it up.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := repos.DefaultPath()
			if err != nil {
				return err
			}
			reg, err := repos.Load(path)
			if err != nil {
				return err
			}
			targets, err := linkTargets(reg, project, all)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			var failed []string
			for _, t := range targets {
				if err := linkOne(t, reg, *brain, home, remove); err != nil {
					failed = append(failed, fmt.Sprintf("%s: %v", t.repo, err))
					continue
				}
				if remove {
					fmt.Fprintf(w, "%s\tunlinked\n", t.repo)
				} else {
					fmt.Fprintf(w, "%s\t-> %s\n", t.repo, link.MemoryDir(*brain, home, t.project))
				}
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if !remove {
				if err := reg.Save(path); err != nil {
					return err
				}
			}
			if len(failed) > 0 {
				return errors.New(strings.Join(failed, "\n"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project to link this repo to")
	cmd.Flags().BoolVar(&all, "all", false, "link every repo that wong knows")
	cmd.Flags().BoolVar(&remove, "remove", false, "go back to Claude Code's own memory folder")
	return cmd
}

func linkTargets(reg repos.Registry, project string, all bool) ([]linkTarget, error) {
	if all {
		if project != "" {
			return nil, errors.New("--all and --project do not go together")
		}
		var targets []linkTarget
		for _, id := range slices.Sorted(maps.Keys(reg)) {
			for _, dir := range reg[id] {
				targets = append(targets, linkTarget{dir, id})
			}
		}
		return targets, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, errors.New("not inside a git repo")
	}
	repo := strings.TrimSpace(string(out))
	if project == "" {
		project = reg.ProjectOf(repo)
	}
	if project == "" {
		return nil, fmt.Errorf("%s is not in any project yet; pass --project ID", repo)
	}
	if _, err := note.ProjectScope(project); err != nil {
		return nil, err
	}
	return []linkTarget{{repo, project}}, nil
}

func linkOne(t linkTarget, reg repos.Registry, brain, home string, remove bool) error {
	if remove {
		return link.Unlink(t.repo)
	}
	if err := os.MkdirAll(filepath.Join(brain, "projects", t.project), 0o755); err != nil {
		return err
	}
	if err := link.Link(t.repo, link.MemoryDir(brain, home, t.project)); err != nil {
		return err
	}
	reg.Add(t.project, t.repo)
	return nil
}
