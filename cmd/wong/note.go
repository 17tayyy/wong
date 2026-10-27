package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/brain"
	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/linear"
	"github.com/17tayyy/wong/internal/link"
	"github.com/17tayyy/wong/internal/note"
	"github.com/17tayyy/wong/internal/repos"
)

func newNoteCmd(home string, brainDir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note",
		Short: "Read, write, verify and move notes in the brain",
		Long: "Read, write, verify and move notes in the brain.\n\n" +
			"A note is given by its path in the brain, its name or its file name without .md.",
	}
	cmd.AddCommand(newNoteShowCmd(home, brainDir), newNoteWriteCmd(brainDir), newNoteVerifyCmd(brainDir), newNoteMoveCmd(brainDir))
	return cmd
}

func newNoteShowCmd(home string, brainDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <note>",
		Short: "Print a note and what wong check finds in it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rel, err := brain.Resolve(*brainDir, args[0])
			if err != nil {
				return err
			}
			data, err := os.ReadFile(filepath.Join(*brainDir, rel))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s\n\n%s", link.MemoryDir(*brainDir, home, rel), data)
			rep, err := checkNote(cmd, *brainDir, rel)
			if err != nil {
				return err
			}
			if len(rep.Findings) > 0 {
				fmt.Fprintln(out, "\nfindings:")
				printFindings(out, rep.Findings)
			}
			return nil
		},
	}
}

func newNoteWriteCmd(brainDir *string) *cobra.Command {
	var to, name, description, kind string
	var sources []string
	cmd := &cobra.Command{
		Use:   "write --to <project|me> --name <name> --description <text> --kind <kind> < body.md",
		Short: "Create a note from its body on stdin and add it to MEMORY.md",
		Long: "Create a note from its body on stdin and add it to the folder's MEMORY.md.\n\n" +
			"--to is a project id or me for global notes. --kind is preference, project,\n" +
			"person, client, decision or reference. --source, repeatable, is linear:<ID>,\n" +
			"file:<repo>/<path>, commit:<repo>@<sha>, note:<name> or a URL.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := note.ParseKind(kind)
			if err != nil {
				return err
			}
			folder, scope, err := brain.Folder(to)
			if err != nil {
				return err
			}
			if err := validSources(sources); err != nil {
				return err
			}
			body, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(body)) == "" {
				return errors.New("write the note's body on stdin")
			}
			n := note.New(name, description, k, scope, "\n"+strings.TrimSpace(string(body))+"\n")
			n.Sources = sources
			data, err := n.Marshal()
			if err != nil {
				return err
			}
			if _, err := note.Parse(data); err != nil {
				return err
			}
			rel, err := brain.Write(*brainDir, folder, n)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "wrote", rel)
			rep, err := checkNote(cmd, *brainDir, rel)
			if err != nil {
				return err
			}
			printFindings(cmd.ErrOrStderr(), rep.Findings)
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "project id, or me for a global note")
	cmd.Flags().StringVar(&name, "name", "", "the note's name")
	cmd.Flags().StringVar(&description, "description", "", "one line: what the note says")
	cmd.Flags().StringVar(&kind, "kind", "", "preference, project, person, client, decision or reference")
	cmd.Flags().StringArrayVar(&sources, "source", nil, "where a claim comes from (repeatable)")
	for _, f := range []string{"to", "name", "description", "kind"} {
		_ = cmd.MarkFlagRequired(f)
	}
	return cmd
}

func newNoteVerifyCmd(brainDir *string) *cobra.Command {
	var sources []string
	cmd := &cobra.Command{
		Use:   "verify <note> [--source <source>]...",
		Short: "Add sources and mark a note verified today, if wong check finds nothing in it",
		Long: "Add sources and mark a note verified today, if wong check finds nothing in it.\n\n" +
			"When something is left, the note is put back as it was and the findings are\n" +
			"listed: fix them and run it again.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rel, err := brain.Resolve(*brainDir, args[0])
			if err != nil {
				return err
			}
			if err := validSources(sources); err != nil {
				return err
			}
			path := filepath.Join(*brainDir, rel)
			original, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			n, err := note.Parse(original)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			for _, s := range sources {
				if !slices.Contains(n.Sources, s) {
					n.Sources = append(n.Sources, s)
				}
			}
			if os.Getenv("LINEAR_API_KEY") == "" && slices.ContainsFunc(n.Sources, func(s string) bool { return strings.HasPrefix(s, "linear:") }) {
				return errors.New("the note cites Linear issues and LINEAR_API_KEY is not set, so they cannot be checked")
			}
			n.Verified, _ = time.Parse(time.DateOnly, time.Now().Format(time.DateOnly))
			data, err := n.Marshal()
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return err
			}
			rep, err := checkNote(cmd, *brainDir, rel)
			if err == nil && len(rep.Findings) > 0 {
				printFindings(cmd.ErrOrStderr(), rep.Findings)
				err = fmt.Errorf("%s still has %d findings; it was left as it was", rel, len(rep.Findings))
			}
			if err != nil {
				if restore := os.WriteFile(path, original, 0o644); restore != nil {
					return errors.Join(err, restore)
				}
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "verified %s on %s\n", rel, n.Verified.Format(time.DateOnly))
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&sources, "source", nil, "a source to add (repeatable)")
	return cmd
}

func newNoteMoveCmd(brainDir *string) *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:   "move <note> --to <project|me>",
		Short: "Move a note to another project or to me, updating its scope and both MEMORY.md",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rel, err := brain.Resolve(*brainDir, args[0])
			if err != nil {
				return err
			}
			dest, err := brain.Move(*brainDir, rel, to)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "moved %s -> %s\n", rel, dest)
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "project id, or me for a global note")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func checkNote(cmd *cobra.Command, brainDir, rel string) (check.Report, error) {
	opts, err := checkOptions(brainDir)
	if err != nil {
		return check.Report{}, err
	}
	opts.Note = rel
	return check.Run(cmd.Context(), opts)
}

func checkOptions(brainDir string) (check.Options, error) {
	path, err := repos.DefaultPath()
	if err != nil {
		return check.Options{}, err
	}
	reg, err := repos.Load(path)
	if err != nil {
		return check.Options{}, err
	}
	opts := check.Options{Brain: brainDir, Repos: reg}
	if key := os.Getenv("LINEAR_API_KEY"); key != "" {
		opts.Tracker = linear.New(key)
	}
	return opts, nil
}

func validSources(sources []string) error {
	for _, s := range sources {
		if !check.ValidSource(s) {
			return fmt.Errorf("source %q not understood: use linear:<ID>, file:<repo>/<path>, commit:<repo>@<sha>, note:<name> or a URL", s)
		}
	}
	return nil
}

func printFindings(out io.Writer, findings []check.Finding) {
	for _, f := range findings {
		fmt.Fprintf(out, "  %s  %s  %s\n", f.Severity, f.Ref, f.Message)
	}
}
