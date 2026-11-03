package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/hook"
	"github.com/17tayyy/wong/internal/linear"
	"github.com/17tayyy/wong/internal/repos"
)

func newHookCmd(home string, brain *string) *cobra.Command {
	var agent string
	var detached bool
	cmd := &cobra.Command{
		Use:       "hook session-start|session-end|pre-tool-use|post-tool-use",
		Short:     "Answer the hooks of Claude Code, Codex and Cursor (wong setup installs them)",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"session-start", "session-end", "pre-tool-use", "post-tool-use"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !slices.Contains([]string{"session-start", "session-end", "pre-tool-use", "post-tool-use"}, args[0]) {
				return fmt.Errorf("unknown hook %q", args[0])
			}
			if !slices.Contains([]hook.Agent{hook.Claude, hook.Codex, hook.Cursor}, hook.Agent(agent)) {
				return fmt.Errorf("unknown agent %q: claude, codex or cursor", agent)
			}
			raw, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "wong hook: reading the hook input:", err)
				return nil
			}
			if args[0] == "session-end" && hook.Agent(agent) != hook.Claude && !detached {
				if err := detach(raw, "hook", "session-end", "--agent", agent, "--detached", "--brain", *brain); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "wong hook:", err)
				}
				return nil
			}
			in, err := hook.ReadInput(raw)
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "wong hook:", err)
				return nil
			}
			if in.CursorVersion != "" && hook.Agent(agent) == hook.Claude {
				fmt.Fprint(cmd.OutOrStdout(), "{}")
				return nil
			}
			env := hook.Env{Agent: hook.Agent(agent), Brain: *brain, Home: home}
			path, err := repos.DefaultPath()
			if err == nil {
				env.Repos, err = repos.Load(path)
			}
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "wong hook:", err)
				return nil
			}
			switch args[0] {
			case "pre-tool-use":
				fmt.Fprint(cmd.OutOrStdout(), hook.FileNotes(in, env))
				return nil
			case "post-tool-use":
				fmt.Fprint(cmd.OutOrStdout(), hook.NoteWritten(in, env))
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
	cmd.Flags().StringVar(&agent, "agent", string(hook.Claude), "the agent that runs the hook: claude, codex or cursor")
	cmd.Flags().BoolVar(&detached, "detached", false, "")
	cmd.Flags().MarkHidden("detached")
	return cmd
}

// detach runs wong again with args and input on its stdin, in a session of its own, and returns without waiting for it.
func detach(input []byte, args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	child := exec.Command(self, args...)
	child.Stdin = r
	child.SysProcAttr = detachedAttr()
	err = child.Start()
	r.Close()
	if err != nil {
		w.Close()
		return err
	}
	_, err = w.Write(input)
	return errors.Join(err, w.Close(), child.Process.Release())
}
