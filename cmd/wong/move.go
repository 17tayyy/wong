package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/seal"
)

func newMoveCmd(brain *string) *cobra.Command {
	var agreed bool
	cmd := &cobra.Command{
		Use:   "move <url>",
		Short: "Move the brain, with its history and its key, to another wong server, and this machine with it",
		Long: "Move the brain to the wong server at url: a new account in wong cloud, or a server of\n" +
			"your own that holds no brain yet. The brain keeps its whole history and, when it is\n" +
			"end-to-end encrypted, its key and its recovery code; a brain in the clear going to\n" +
			"a server that keeps brains sealed is sealed on the way, with a new recovery code.\n" +
			"The old server keeps its copy until you delete it there, and tells your other\n" +
			"machines where the brain went: run wong setup <url> on each.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			to := strings.TrimSuffix(args[0], "/")
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			old, err := requireRemote(60 * time.Second)
			if err != nil {
				return err
			}
			if strings.TrimSuffix(old.URL, "/") == to {
				return errors.New("the brain already lives at " + to)
			}
			if err := old.Unlock(ctx); err != nil {
				return err
			}
			if err := syncBrain(cmd, old, *brain, true, true); err != nil {
				return err
			}
			if st, err := remote.LoadState(*brain); err != nil {
				return err
			} else if len(st.Conflicts) > 0 {
				return errors.New("some files changed both here and on the server: resolve them with wong resolve before moving")
			}

			fmt.Fprintf(out, "\nConnecting this machine to %s.\n", to)
			next, err := setRemoteClient(cmd, to)
			if err != nil {
				return err
			}
			next.Device = old.Device
			keys, err := next.Keys(ctx)
			if err != nil {
				return fmt.Errorf("the server at %s did not answer as expected: %w", to, err)
			}
			if keys.Sealed {
				return errors.New("the brain at " + to + " is already end-to-end encrypted, so it is not empty: move into an empty one")
			}
			if held, _, err := next.Changes(ctx, 0); err != nil {
				return err
			} else if len(held) > 0 {
				return errors.New("the brain at " + to + " already holds files: move into an empty one")
			}

			var key, seals *seal.Key
			switch {
			case old.Key != nil:
				was, err := old.Keys(ctx)
				if err != nil {
					return err
				}
				if err := next.StartSealAs(ctx, next.Device, old.Key, was.RecoveryKey, was.Recovery); err != nil {
					return err
				}
				key = old.Key
			case keys.Required:
				if !agreed && !confirm(cmd, "The brain is in the clear, and "+to+" keeps brains end-to-end encrypted: it is sealed on the way, with a new recovery code. Go on?") {
					return errors.New("the brain was not moved")
				}
				if key, err = sealNew(cmd, next); err != nil {
					return err
				}
				seals = key
			}

			moved, err := remote.CopyHistory(ctx, old, next, seals, func(n int) { fmt.Fprintf(cmd.ErrOrStderr(), "\rmoved %d versions", n) })
			fmt.Fprintln(cmd.ErrOrStderr())
			if err != nil {
				return fmt.Errorf("the brain was not fully moved, and still lives at %s: %w", old.URL, err)
			}
			if err := old.SetMoved(ctx, to); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s was not told the brain moved: %v\n", old.URL, err)
			}
			path, err := remote.ConfigPath()
			if err != nil {
				return err
			}
			if err := remote.SaveConfig(path, remote.Config{URL: next.URL, Token: next.Token}); err != nil {
				return err
			}
			next.Key = key
			if err := syncBrain(cmd, next, *brain, true, true); err != nil {
				return err
			}
			fmt.Fprintf(out, "\nMoved %d versions of the brain to %s, and this machine with it.\n"+
				"The old server keeps its copy until you delete it there; it tells your other machines where the brain went.\n"+
				"On each of them, run wong setup %s, then approve it from here with wong keys approve.\n", moved, to, to)
			return nil
		},
	}
	cmd.Flags().BoolVar(&agreed, "yes", false, "seal a brain in the clear on the way without asking")
	return cmd
}

// setRemoteClient connects this machine to the server at url, approved in a browser or with a token on stdin, without
// making it this machine's server yet.
func setRemoteClient(cmd *cobra.Command, url string) (*remote.Client, error) {
	if err := remote.CheckURL(url); err != nil {
		return nil, err
	}
	in, terminal := cmd.InOrStdin().(*os.File)
	if !terminal || !isTerminal(in) {
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if token := strings.TrimSpace(line); token != "" {
			return remote.NewClient(url, token, 60*time.Second), nil
		}
		return nil, errors.Join(errors.New("pass the new server's token on stdin, or run it in a terminal to approve this machine"), err)
	}
	name, _ := os.Hostname()
	token, err := approvedToken(cmd.Context(), remote.NewClient(url, "", 30*time.Second), name, cmd.ErrOrStderr(), typedLine(in))
	if err != nil {
		return nil, err
	}
	return remote.NewClient(url, token, 60*time.Second), nil
}

// confirm asks a yes or no question on the terminal; anything but a yes, or no terminal, is a no.
func confirm(cmd *cobra.Command, question string) bool {
	in, ok := cmd.InOrStdin().(*os.File)
	if !ok || !isTerminal(in) {
		return false
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N] ", question)
	answer, _ := bufio.NewReader(in).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(answer))
	return a == "y" || a == "yes"
}

// sealNew makes a key and a recovery code for a brain moving in the clear to a server that keeps brains sealed, and
// seals the server's empty brain with them.
func sealNew(cmd *cobra.Command, client *remote.Client) (*seal.Key, error) {
	key, err := seal.NewKey()
	if err != nil {
		return nil, err
	}
	code := seal.NewRecovery()
	recovery, err := seal.RecoveryDevice(code)
	if err != nil {
		return nil, err
	}
	if err := client.StartSeal(cmd.Context(), client.Device, recovery, key, 0); err != nil {
		return nil, err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nThe brain is end-to-end encrypted on its new server. Its recovery code:\n\n    %s\n\n"+
		"Write it down and keep it away from this machine. It opens the brain on a new machine if you lose every one\n"+
		"you have; without it and without them, nobody can open the brain, not even the server.\n", code)
	return key, nil
}
