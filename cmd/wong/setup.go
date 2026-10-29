package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/link"
	"github.com/17tayyy/wong/internal/repos"
)

func newSetupCmd(home string, brain *string) *cobra.Command {
	var maps []string
	cmd := &cobra.Command{
		Use:   "setup <server-url>",
		Short: "Connect this machine to a wong server, bring its memory into the brain and point Claude Code at it",
		Long: "Connect this machine to a wong server and make the brain its memory, in one go:\n\n" +
			"  1. saves the server and this machine's token, made on the server with\n" +
			"     wong serve --new-token <name> and pasted when asked or read from stdin;\n" +
			"  2. pulls the brain, which is empty until the first machine pushes;\n" +
			"  3. imports this machine's Claude Code memory into it, as wong import does;\n" +
			"  4. pushes it, so the server holds the memory of every machine;\n" +
			"  5. links every repo it imported to its project, and every other folder to me/.\n\n" +
			"wong import --dry-run shows first how the memory will be grouped into projects;\n" +
			"--map changes it. Then install the Claude Code plugin.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := setRemote(cmd, args[0])
			if err != nil {
				return err
			}
			if err := syncBrain(cmd, client, *brain, true, false); err != nil {
				return fmt.Errorf("the server did not answer as expected, check the url and the token: %w", err)
			}
			from := filepath.Join(claudeConfigDir(home), "projects")
			if _, err := os.Stat(from); errors.Is(err, fs.ErrNotExist) {
				fmt.Fprintln(cmd.OutOrStdout(), "no Claude Code memory on this machine to import")
			} else if err := runImport(cmd, home, *brain, from, maps, false); err != nil {
				return err
			}
			if err := syncBrain(cmd, client, *brain, false, true); err != nil {
				return err
			}
			if err := linkEverything(cmd, *brain, home); err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), "\nNow install the Claude Code plugin, then restart open sessions:\n"+
				"  claude plugin marketplace add 17tayyy/wong\n"+
				"  claude plugin install wong@wong\n")
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&maps, "map", nil, "send a directory or memory folder to a project: PATH=ID (ID \"me\" for global notes)")
	return cmd
}

// linkEverything links every repo wong knows to its project and makes me/ the memory of every other folder.
func linkEverything(cmd *cobra.Command, brain, home string) error {
	path, err := repos.DefaultPath()
	if err != nil {
		return err
	}
	reg, err := repos.Load(path)
	if err != nil {
		return err
	}
	targets, err := linkTargets(reg, "", true)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	for _, t := range targets {
		if err := linkOne(t, reg, brain, home, false); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", t.dir, err)
			continue
		}
		fmt.Fprintf(out, "%s  -> %s\n", t.dir, link.MemoryDir(brain, home, "projects/"+t.project))
	}
	if err := reg.Save(path); err != nil {
		return err
	}
	return linkGlobal(out, brain, home, false)
}
