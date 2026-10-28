package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/hook"
	"github.com/17tayyy/wong/internal/linear"
	"github.com/17tayyy/wong/internal/repos"
)

func newHookCmd(home string, brain *string) *cobra.Command {
	return &cobra.Command{
		Use:       "hook session-start|session-end|pre-tool-use",
		Short:     "Answer Claude Code's hooks (the wong plugin calls it)",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"session-start", "session-end", "pre-tool-use"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !slices.Contains([]string{"session-start", "session-end", "pre-tool-use"}, args[0]) {
				return fmt.Errorf("unknown hook %q", args[0])
			}
			var in hook.Input
			if err := json.NewDecoder(cmd.InOrStdin()).Decode(&in); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "wong hook: reading the hook input:", err)
				return nil
			}
			env := hook.Env{Brain: *brain, Home: home}
			path, err := repos.DefaultPath()
			if err == nil {
				env.Repos, err = repos.Load(path)
			}
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "wong hook:", err)
				return nil
			}
			if args[0] == "pre-tool-use" {
				fmt.Fprint(cmd.OutOrStdout(), hook.FileNotes(in, env))
				return nil
			}
			if key := os.Getenv("LINEAR_API_KEY"); key != "" {
				env.Tracker = linear.New(key)
			}
			if env.Remote, err = remoteClient(5 * time.Second); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "wong hook:", err)
			}
			if args[0] == "session-start" {
				fmt.Fprint(cmd.OutOrStdout(), hook.SessionStart(cmd.Context(), in, env))
				return nil
			}
			if err := hook.SessionEnd(cmd.Context(), in, env); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "wong hook:", err)
			}
			return nil
		},
	}
}
