package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/check"
)

func newCheckCmd(brain *string) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check every note against the files, commits, issues and notes it cites",
		Long: "Check every note against the files, commits, issues and notes it cites.\n\n" +
			"stale   the note is wrong: a file it cites was deleted, an issue it calls open is\n" +
			"        closed, a note it links to does not exist, or a deploy it leaves work for\n" +
			"        (pending: release or main) happened.\n" +
			"review  the note may be outdated: a file or commit cannot be found, a commit is on\n" +
			"        no remote branch, an issue closed after the note was last updated, a tag is a\n" +
			"        spelling of another, or its modified or verified date is later than the file.\n\n" +
			"Files and commits are looked up in the repos that wong import registered for each\n" +
			"project. Linear issues are checked when LINEAR_API_KEY holds a personal API key.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := checkOptions(*brain)
			if err != nil {
				return err
			}
			opts.Project = project
			rep, err := check.Run(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return printReport(cmd.OutOrStdout(), rep)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "check only this project")
	return cmd
}

func printReport(out io.Writer, rep check.Report) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	stale, review := map[string]bool{}, map[string]bool{}
	for i, f := range rep.Findings {
		if i == 0 || f.Note != rep.Findings[i-1].Note {
			if i > 0 {
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, f.Note)
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\n", f.Severity, f.Ref, f.Message)
		if f.Severity == check.Stale {
			stale[f.Note] = true
		} else {
			review[f.Note] = true
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	toReview := 0
	for n := range review {
		if !stale[n] {
			toReview++
		}
	}
	if len(rep.Findings) > 0 {
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "checked %s: %d stale, %d to review, %d with no findings\n",
		notes(rep.Notes), len(stale), toReview, rep.Notes-len(stale)-toReview)
	for _, s := range rep.Skipped {
		fmt.Fprintln(out, "skipped:", s)
	}
	return nil
}
