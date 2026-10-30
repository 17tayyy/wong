package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/link"
	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/repos"
)

func newSetupCmd(home string, brain *string) *cobra.Command {
	var maps []string
	cmd := &cobra.Command{
		Use:   "setup [server-url]",
		Short: "Make the brain this machine's memory, connected to a wong server or on its own",
		Long: "Make the brain this machine's memory, in one go:\n\n" +
			"  1. with a server, connects this machine: it shows a code to enter in the\n" +
			"     dashboard, in a browser signed in to wong, or takes a token made on the\n" +
			"     server with wong serve --new-token <name>, pasted or read from stdin;\n" +
			"     seals a new brain on a server that keeps them end-to-end encrypted, or\n" +
			"     waits for another machine to give this one the key of a sealed brain;\n" +
			"     then pulls the brain, which is empty until the first machine pushes;\n" +
			"  2. imports this machine's Claude Code memory into it, as wong import does;\n" +
			"  3. with a server, pushes it, so the server holds the memory of every machine;\n" +
			"  4. links every repo it imported to its project, and every other folder to me/;\n" +
			"  5. installs the wong plugin for Claude Code.\n\n" +
			"wong import --dry-run shows first how the memory will be grouped into projects;\n" +
			"--map changes it. Run it again with a url to connect a machine set up on its own.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var client *remote.Client
			if len(args) == 1 {
				var err error
				if client, err = setRemote(cmd, args[0]); err != nil {
					return err
				}
				if client.Device, err = remote.LoadDevice(); err != nil {
					return err
				}
				if err := prepareKeys(cmd, client, *brain); err != nil {
					return err
				}
				if err := syncBrain(cmd, client, *brain, true, false); err != nil {
					return fmt.Errorf("the server did not answer as expected, check the url and the token: %w", err)
				}
			}
			from := filepath.Join(claudeConfigDir(home), "projects")
			if _, err := os.Stat(from); errors.Is(err, fs.ErrNotExist) {
				fmt.Fprintln(cmd.OutOrStdout(), "no Claude Code memory on this machine to import")
			} else if err := runImport(cmd, home, *brain, from, maps, false); err != nil {
				return err
			}
			if client != nil {
				if err := syncBrain(cmd, client, *brain, false, true); err != nil {
					return err
				}
			}
			if err := linkEverything(cmd, *brain, home); err != nil {
				return err
			}
			installPlugin(cmd)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&maps, "map", nil, "send a directory or memory folder to a project: PATH=ID (ID \"me\" for global notes)")
	return cmd
}

// prepareKeys seals a new brain on a server that keeps them sealed, or waits for this machine's key to a sealed one.
func prepareKeys(cmd *cobra.Command, client *remote.Client, brain string) error {
	keys, err := client.Keys(cmd.Context())
	if err != nil {
		return fmt.Errorf("the server did not answer as expected, check the url and the token: %w", err)
	}
	switch {
	case keys.Sealed && keys.Wrapped == nil:
		return waitForKey(cmd, client)
	case !keys.Sealed && keys.Required:
		return sealBrain(cmd, client, brain, false)
	}
	return nil
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

var pluginSteps = [][]string{{"plugin", "marketplace", "add", "17tayyy/wong"}, {"plugin", "install", "wong@wong"}}

// installPlugin installs the wong plugin with Claude Code's own command, or says how to once Claude Code is there.
func installPlugin(cmd *cobra.Command) {
	out := cmd.OutOrStdout()
	manual := "  claude " + strings.Join(pluginSteps[0], " ") + "\n  claude " + strings.Join(pluginSteps[1], " ") + "\n"
	claude, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprint(out, "\nClaude Code is not on this machine's PATH. Once it is, install the wong plugin:\n"+manual)
		return
	}
	for i, step := range pluginSteps {
		result, err := exec.Command(claude, step...).CombinedOutput()
		if err != nil && (i > 0 || !strings.Contains(strings.ToLower(string(result)), "already")) {
			fmt.Fprintf(out, "\nInstalling the wong plugin failed (%s). Install it by hand:\n%s", strings.TrimSpace(string(result)), manual)
			return
		}
	}
	fmt.Fprintln(out, "\nInstalled the wong plugin for Claude Code. Restart open sessions to pick everything up.")
}
