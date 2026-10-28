package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/graph"
)

func newTagsCmd(brainDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "tags",
		Short: "List the tags in use, most used first, to reuse them instead of new spellings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g, err := graph.Load(*brainDir)
			if err != nil {
				return err
			}
			for _, t := range g.TagList(0) {
				fmt.Fprintln(cmd.OutOrStdout(), t)
			}
			return nil
		},
	}
}
