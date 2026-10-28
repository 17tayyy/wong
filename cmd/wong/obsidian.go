package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/note"
	"github.com/17tayyy/wong/internal/obsidian"
)

const claudeIndexLines = 200

func newObsidianCmd(brainDir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "obsidian",
		Short: "Copy the brain into an Obsidian vault, or bring a vault's notes into the brain",
		Long: "Copy the brain into an Obsidian vault, or bring a vault's notes into the brain.\n\n" +
			"The brain itself opens as a vault too: Obsidian keeps its settings in .obsidian, which\n" +
			"wong push leaves on each machine, and notes whose name is not their file name carry it\n" +
			"as an alias, so [[name]] links resolve in Obsidian as they do for agents.",
	}
	export := &cobra.Command{
		Use:   "export <folder>",
		Short: "Copy the brain's notes and MEMORY.md indexes into a new folder that Obsidian opens as a vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rep, err := obsidian.Export(*brainDir, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "exported %d notes and %d indexes to %s", rep.Notes, rep.Indexes, args[0])
			if rep.Links > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "; %d links that named a note now name its file", rep.Links)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			return nil
		},
	}
	var to, kind string
	var dryRun bool
	imp := &cobra.Command{
		Use:   "import <vault> --to <project|me>",
		Short: "Bring the notes of an Obsidian vault onto a shelf of the brain",
		Long: "Bring the notes of an Obsidian vault onto a shelf of the brain, from every folder but\n" +
			"Obsidian's own, flattened. Each note is named after its file and keeps its properties;\n" +
			"its description comes from its description property or its first line, and its tags\n" +
			"from its tags property and the #tags in its text. Notes the brain already has are left\n" +
			"as they are. Attachments are not brought; the import lists them, and the links that\n" +
			"reach no note. --dry-run shows all of it without writing.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := note.ParseKind(kind)
			if err != nil {
				return err
			}
			rep, err := obsidian.Import(args[0], *brainDir, to, k, dryRun)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			written := 0
			for _, n := range rep.Notes {
				if n.Skipped != "" {
					fmt.Fprintf(out, "  skipped %s: %s\n", n.From, n.Skipped)
					continue
				}
				written++
				fmt.Fprintf(out, "  %s -> %s\n", n.From, n.To)
			}
			verb := "imported"
			if dryRun {
				verb = "would import"
			}
			fmt.Fprintf(out, "%s %d of %d notes\n", verb, written, len(rep.Notes))
			if len(rep.Attachments) > 0 {
				fmt.Fprintf(out, "%d attachments were not brought: %s\n", len(rep.Attachments), sample(rep.Attachments, 8))
			}
			if len(rep.Missing) > 0 {
				fmt.Fprintf(out, "%d links reach no note; wong check will report them:\n  %s\n", len(rep.Missing), strings.Join(rep.Missing, "\n  "))
			}
			if rep.IndexLines > claudeIndexLines {
				fmt.Fprintf(out, "the shelf's MEMORY.md will have %d lines, and Claude Code loads only the first %d: consider importing onto a project of its own\n", rep.IndexLines, claudeIndexLines)
			}
			if !dryRun && written > 0 {
				fmt.Fprintln(out, "run wong push to send them to the server, or let the session-end hook do it")
			}
			return nil
		},
	}
	imp.Flags().StringVar(&to, "to", "", "project id, or me for notes that apply everywhere")
	imp.Flags().StringVar(&kind, "kind", string(note.Reference), "kind of the notes whose properties do not say")
	imp.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be imported without writing")
	_ = imp.MarkFlagRequired("to")
	cmd.AddCommand(export, imp)
	return cmd
}

func sample(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + fmt.Sprintf(" and %d more", len(items)-n)
}
