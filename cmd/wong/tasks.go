package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/remote"
)

func newTasksCmd(brain *string) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "tasks",
		Short: "List the tasks the server's webhooks left on notes",
		Long: "List the tasks the server's webhooks left on notes.\n\n" +
			"A push to a repo's default branch that removes a file a note cites, a Linear issue\n" +
			"a note cites that becomes completed or canceled, and a new v* tag or push to the\n" +
			"default branch that a note leaves work for (pending: release or main) leave a task\n" +
			"on the note.\n" +
			"A task closes by itself when the note reaches the server and no longer breaks the\n" +
			"rule: fix it with the wong:maintain-brain skill and run wong push (the session-end\n" +
			"hook does). The server of a sealed brain keeps only the events, and this machine\n" +
			"applies them to its notes and keeps the tasks.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := requireRemote(30 * time.Second)
			if err != nil {
				return err
			}
			tasks, err := remote.OpenTasks(cmd.Context(), client, *brain, project)
			if err != nil {
				return err
			}
			return printTasks(cmd.OutOrStdout(), tasks)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "list only this project's tasks (me for the global notes)")
	return cmd
}

func printTasks(out io.Writer, tasks []remote.Task) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for i, t := range tasks {
		if i == 0 || t.Note != tasks[i-1].Note {
			if i > 0 {
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, t.Note)
		}
		fmt.Fprintf(w, "  %s\t%s\t%s (%s)\n", t.Severity, t.Ref, t.Message, t.Source)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if len(tasks) > 0 {
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "%d open tasks\n", len(tasks))
	return nil
}
