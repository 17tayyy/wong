package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/importer"
	"github.com/17tayyy/wong/internal/repos"
)

func newImportCmd(home string, brain *string) *cobra.Command {
	var from string
	var maps []string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import Claude Code's memory folders into the brain",
		Long: "Import Claude Code's memory folders into the brain.\n\n" +
			"Repos that share a parent directory become one project (~/acme/* -> acme),\n" +
			"a repo directly under home is its own project, and home itself goes to me/.\n" +
			"Run it again to bring what changed in Claude Code since: notes the brain has not\n" +
			"touched are updated, notes changed in the brain are kept, and notes changed on both\n" +
			"sides are reported. Nothing in ~/.claude is changed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			overrides, err := parseMaps(maps)
			if err != nil {
				return err
			}
			sources, err := importer.Plan(from, home, overrides)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			for _, s := range sources {
				path := s.Path
				if path == "" {
					path = s.Folder
				}
				fmt.Fprintf(w, "%s\t-> %s\t%s\n", path, s.Target, notes(len(s.Notes)))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			res, err := importer.Apply(*brain, sources, dryRun)
			for _, warning := range res.Warnings {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", warning)
			}
			if err != nil {
				return err
			}
			verb := "dry run: would import"
			if !dryRun {
				verb = "imported"
				if err := recordRepos(sources); err != nil {
					return err
				}
			}
			fmt.Fprintf(out, "%s %s into %s: %d updated, %d unchanged, %d changed in the brain and kept\n",
				verb, notes(res.Written), *brain, res.Updated, res.Unchanged, res.Kept)
			for _, rel := range res.Conflicts {
				fmt.Fprintf(cmd.ErrOrStderr(), "conflict: %s changed both in the brain and in Claude Code; left as it is\n", rel)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", filepath.Join(home, ".claude", "projects"), "Claude Code projects directory")
	cmd.Flags().StringArrayVar(&maps, "map", nil, "send a directory or memory folder to a project: PATH=ID (ID \"me\" for global notes)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the plan and check for conflicts without writing")
	return cmd
}

func recordRepos(sources []importer.Source) error {
	path, err := repos.DefaultPath()
	if err != nil {
		return err
	}
	reg, err := repos.Load(path)
	if err != nil {
		return err
	}
	for _, s := range sources {
		if id, ok := strings.CutPrefix(s.Target, "projects/"); ok && s.Repo != "" {
			reg.Add(id, s.Repo)
		}
	}
	return reg.Save(path)
}

func notes(n int) string {
	if n == 1 {
		return "1 note"
	}
	return fmt.Sprintf("%d notes", n)
}

func parseMaps(maps []string) (map[string]string, error) {
	out := map[string]string{}
	for _, m := range maps {
		i := strings.LastIndex(m, "=")
		if i <= 0 || i == len(m)-1 {
			return nil, fmt.Errorf("--map %q: want PATH=ID", m)
		}
		key := m[:i]
		if abs, err := filepath.Abs(key); err == nil && !strings.HasPrefix(key, "-") {
			key = abs
		}
		out[key] = m[i+1:]
	}
	return out, nil
}
