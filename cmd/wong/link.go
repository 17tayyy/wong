package main

import (
	"errors"
	"fmt"
	"io"
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
	dir, project string
}

func newLinkCmd(home string, brain *string) *cobra.Command {
	var project string
	var all, global, remove bool
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Make Claude Code keep its memory in the brain",
		Long: "Make Claude Code keep its memory in the brain.\n\n" +
			"Sets autoMemoryDirectory in .claude/settings.local.json of the current repo (or\n" +
			"folder, with --project) to the project's folder in the brain, so sessions there\n" +
			"read and write the brain instead of ~/.claude/projects. The project comes from\n" +
			"--project or from the repos that wong import registered. --global sets me/ as the\n" +
			"memory of every folder that is not linked. Restart open sessions to pick it up.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if global {
				if all || project != "" {
					return errors.New("--global goes alone")
				}
				return linkGlobal(out, *brain, home, remove)
			}
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
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			var failed []string
			for _, t := range targets {
				if err := linkOne(t, reg, *brain, home, remove); err != nil {
					failed = append(failed, fmt.Sprintf("%s: %v", t.dir, err))
					continue
				}
				if remove {
					fmt.Fprintf(w, "%s\tunlinked\n", t.dir)
				} else {
					fmt.Fprintf(w, "%s\t-> %s\n", t.dir, link.MemoryDir(*brain, home, "projects/"+t.project))
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
	cmd.Flags().StringVar(&project, "project", "", "project to link this repo or folder to")
	cmd.Flags().BoolVar(&all, "all", false, "link every repo and folder that wong knows")
	cmd.Flags().BoolVar(&global, "global", false, "use me/ as the memory of every folder that is not linked")
	cmd.Flags().BoolVar(&remove, "remove", false, "go back to Claude Code's own memory folder")
	return cmd
}

func linkGlobal(out io.Writer, brain, home string, remove bool) error {
	settings := filepath.Join(claudeConfigDir(home), "settings.json")
	if remove {
		if err := link.Unset(settings, false); err != nil {
			return err
		}
		_, err := fmt.Fprintf(out, "%s: no default memory\n", settings)
		return err
	}
	if err := os.MkdirAll(filepath.Join(brain, "me"), 0o755); err != nil {
		return err
	}
	dir := link.MemoryDir(brain, home, "me")
	if err := link.Set(settings, dir); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "%s  -> %s\n", settings, dir)
	return err
}

func claudeConfigDir(home string) string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(home, ".claude")
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
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output(); err == nil {
		dir = strings.TrimSpace(string(out))
	}
	if project == "" {
		project = reg.ProjectOf(dir)
	}
	if project == "" {
		return nil, fmt.Errorf("%s is not in any project yet; pass --project ID", dir)
	}
	if _, err := note.ProjectScope(project); err != nil {
		return nil, err
	}
	return []linkTarget{{dir, project}}, nil
}

func linkOne(t linkTarget, reg repos.Registry, brain, home string, remove bool) error {
	if remove {
		return link.Unlink(t.dir)
	}
	if err := os.MkdirAll(filepath.Join(brain, "projects", t.project), 0o755); err != nil {
		return err
	}
	if err := link.Link(t.dir, link.MemoryDir(brain, home, "projects/"+t.project)); err != nil {
		return err
	}
	reg.Add(t.project, t.dir)
	return nil
}
