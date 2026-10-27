package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/repos"
	"github.com/17tayyy/wong/internal/skills"
)

func newSkillsCmd(home string, brain *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Keep the user's skills in the brain",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "save",
		Short: "Record where each skill comes from and copy the ones with no source into the brain",
		Long: "Record where each skill comes from and copy the ones with no source into the brain.\n\n" +
			"Global skills installed with npx skills are recorded by source in\n" +
			"skills/skills-lock.json. Skills with no source, and project skills that git does\n" +
			"not carry with their repo, are copied. The plugin's session-end hook runs it.",
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
			res, err := skills.Save(skills.Env{Brain: *brain, Home: home, Repos: reg})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "recorded by source: %d\n", len(res.Sourced))
			for _, s := range res.Sourced {
				fmt.Fprintln(out, "  ", s)
			}
			fmt.Fprintf(out, "copied into the brain: %d\n", len(res.Copied))
			for _, c := range res.Copied {
				fmt.Fprintln(out, "  ", c)
			}
			return nil
		},
	})
	return cmd
}
