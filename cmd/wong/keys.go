package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/seal"
)

func newKeysCmd(brain *string) *cobra.Command {
	keys := &cobra.Command{
		Use:   "keys",
		Short: "See the end-to-end encryption of the brain on the server, and this machine's code",
		Long: "A sealed brain is end-to-end encrypted: the server keeps only what it cannot read,\n" +
			"and the key lives on your machines. wong keys says whether the brain is sealed,\n" +
			"whether this machine holds its key, which machines wait for theirs, and the code\n" +
			"this machine shows while another approves it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := requireRemote(30 * time.Second)
			if err != nil {
				return err
			}
			if err := client.Unlock(cmd.Context()); err != nil && !errors.Is(err, remote.ErrLocked) {
				return err
			}
			keys, err := client.Keys(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch {
			case keys.Sealed && keys.Wrapped != nil:
				fmt.Fprintln(out, "The brain is end-to-end encrypted, and this machine holds its key.")
			case keys.Sealed:
				fmt.Fprintln(out, "The brain is end-to-end encrypted, and this machine has no key yet. It asked for one: approve\nit from a machine that has the key with wong keys approve, checking it shows the code below,\nor use the recovery code here with wong keys recover.")
			case keys.Required:
				fmt.Fprintln(out, "The brain is not encrypted yet, and this server keeps every brain sealed: run wong keys seal.")
			default:
				fmt.Fprintln(out, "The brain is not end-to-end encrypted.")
			}
			switch {
			case keys.Sealed && keys.CloudAIs:
				fmt.Fprintln(out, "The AIs that run in the cloud can read it; wong keys cloud-ai off stops them.")
			case keys.Sealed:
				fmt.Fprintln(out, "The AIs that run in the cloud, like claude.ai, cannot read it; wong keys cloud-ai on lets them.")
			}
			fmt.Fprintf(out, "This machine's code: %s\n", seal.Fingerprint(client.Device.Public()))
			if keys.Wrapped != nil && len(keys.Pending) > 0 {
				fmt.Fprintln(out, "\nWaiting for the key:")
				for _, p := range keys.Pending {
					fmt.Fprintf(out, "  %-20s %s\n", p.Device, seal.Fingerprint(p.Public))
				}
				fmt.Fprintln(out, "\nApprove one with wong keys approve <machine>, once it shows the same code.")
			}
			return nil
		},
	}
	keys.AddCommand(newKeysSealCmd(brain), newKeysApproveCmd(), newKeysRecoverCmd(), newKeysCloudCmd())
	return keys
}

func newKeysSealCmd(brain *string) *cobra.Command {
	var agreed bool
	cmd := &cobra.Command{
		Use:   "seal",
		Short: "Make the brain on the server end-to-end encrypted, from this machine",
		Long: "Make the brain on the server end-to-end encrypted: this machine makes its key\n" +
			"and a recovery code, then pushes the brain encrypted. A brain the server holds in\n" +
			"the clear is pulled first; its clear copies and their earlier versions are then\n" +
			"deleted from the server, which keeps only the encrypted ones. Other machines sync\n" +
			"again once one that holds the key approves them.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := requireRemote(30 * time.Second)
			if err != nil {
				return err
			}
			if err := sealBrain(cmd, client, *brain, agreed); err != nil {
				return err
			}
			return syncBrain(cmd, client, *brain, false, true)
		},
	}
	cmd.Flags().BoolVar(&agreed, "yes", false, "replace a brain the server holds in the clear without asking")
	return cmd
}

func newKeysApproveCmd() *cobra.Command {
	var code string
	cmd := &cobra.Command{
		Use:   "approve [machine]",
		Short: "Give a machine waiting for the brain's key its key, once it shows the same code",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := requireRemote(30 * time.Second)
			if err != nil {
				return err
			}
			if err := client.Unlock(cmd.Context()); err != nil {
				return err
			}
			if client.Key == nil {
				return errors.New("the brain is not end-to-end encrypted: there is no key to give")
			}
			keys, err := client.Keys(cmd.Context())
			if err != nil {
				return err
			}
			var waiting []remote.KeyRequest
			for _, p := range keys.Pending {
				if len(args) == 0 || p.Device == args[0] {
					waiting = append(waiting, p)
				}
			}
			if len(waiting) == 0 {
				return errors.New("no machine waits for the key")
			}
			in, terminal := cmd.InOrStdin().(*os.File)
			terminal = terminal && isTerminal(in)
			reader := bufio.NewReader(cmd.InOrStdin())
			out := cmd.OutOrStdout()
			for _, p := range waiting {
				fp := seal.Fingerprint(p.Public)
				switch {
				case code != "":
					if digits(code) != digits(fp) {
						return fmt.Errorf("%s shows another code than the one you gave: do not approve it", p.Device)
					}
				case terminal:
					fmt.Fprintf(out, "%s waits for the brain's key. Check that %s shows this code:\n\n    %s\n\nApprove %s? [y/N] ", p.Device, p.Device, fp, p.Device)
					answer, _ := reader.ReadString('\n')
					if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
						fmt.Fprintf(out, "%s not approved\n", p.Device)
						continue
					}
				default:
					return errors.New("run it in a terminal to compare the codes, or pass the code the machine shows with --code")
				}
				if err := client.ApproveKey(cmd.Context(), p, client.Key); err != nil {
					return err
				}
				fmt.Fprintf(out, "%s has the brain's key\n", p.Device)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&code, "code", "", "the code the waiting machine shows, to approve it without asking")
	return cmd
}

func newKeysCloudCmd() *cobra.Command {
	var agreed bool
	cmd := &cobra.Command{
		Use:   "cloud-ai [on|off]",
		Short: "Let the AIs that run in the cloud read the encrypted brain, or stop them",
		Long: "The AIs that run on your machines read the brain there. Those that run in the\n" +
			"cloud, like claude.ai, ChatGPT or a cloud agent connected to wong over MCP, can\n" +
			"only read it through the server: on gives the server the brain's key to open it\n" +
			"for them, call by call, and each one still asks you before it connects. While it\n" +
			"is on, the server can read the brain. Off makes the server forget the key.",
		Args:      cobra.MatchAll(cobra.MaximumNArgs(1), cobra.OnlyValidArgs),
		ValidArgs: []string{"on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			client, err := requireRemote(30 * time.Second)
			if err != nil {
				return err
			}
			if err := client.Unlock(ctx); err != nil {
				return err
			}
			keys, err := client.Keys(ctx)
			if err != nil {
				return err
			}
			if !keys.Sealed {
				return errors.New("the brain is not end-to-end encrypted: the server reads it already")
			}
			switch {
			case len(args) == 0 && keys.CloudAIs:
				fmt.Fprintln(out, "The AIs that run in the cloud can read the brain; wong keys cloud-ai off stops them.")
			case len(args) == 0:
				fmt.Fprintln(out, "The AIs that run in the cloud cannot read the brain; wong keys cloud-ai on lets them.")
			case args[0] == "off":
				if err := client.StopCloudAIs(ctx); err != nil {
					return err
				}
				fmt.Fprintln(out, "The server forgot the brain's key: the AIs that run in the cloud cannot read it any more.")
			default:
				if !agreed {
					in, ok := cmd.InOrStdin().(*os.File)
					if !ok || !isTerminal(in) {
						return errors.New("run it in a terminal to agree, or pass --yes")
					}
					fmt.Fprint(out, "While this is on, the wong server can open the brain to answer the AIs you connect to it, like\n"+
						"claude.ai or ChatGPT, and each AI sends what it reads to the company that runs it. Let them in? [y/N] ")
					answer, _ := bufio.NewReader(in).ReadString('\n')
					if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
						return errors.New("the AIs that run in the cloud still cannot read the brain")
					}
				}
				if err := client.LetCloudAIs(ctx, client.Key); err != nil {
					return err
				}
				fmt.Fprintf(out, "The AIs that run in the cloud can read the brain now. Connect one to %s/mcp: it asks you to\napprove it in the browser.\n", client.URL)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&agreed, "yes", false, "let them in without asking")
	return cmd
}

func newKeysRecoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recover",
		Short: "Give this machine the brain's key with the recovery code",
		Long: "Give this machine the brain's key with the recovery code you wrote down when the\n" +
			"brain was sealed: for when no other machine is there to approve it. The code is\n" +
			"read from the terminal, or from stdin.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := requireRemote(30 * time.Second)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.ErrOrStderr(), "Recovery code: ")
			line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			if err := recoverKey(cmd.Context(), client, line); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "this machine has the brain's key")
			return nil
		},
	}
}

// sealBrain makes the server's brain end-to-end encrypted from this machine, shows the recovery code, and leaves the
// client ready to push the brain encrypted. A brain the server holds in the clear is pulled first, and once the person
// agrees its clear copies and their history go from the server, for the push to send them again encrypted.
func sealBrain(cmd *cobra.Command, client *remote.Client, brain string, agreed bool) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	in, terminal := cmd.InOrStdin().(*os.File)
	terminal = terminal && isTerminal(in)
	reader := bufio.NewReader(cmd.InOrStdin())
	if _, err := remote.Pull(ctx, client, brain); err != nil {
		return err
	}
	st, err := remote.LoadState(brain)
	if err != nil {
		return err
	}
	if len(st.Conflicts) > 0 {
		return errors.New("some files changed both here and on the server: resolve them with wong resolve before sealing, which replaces the server's copies")
	}
	if len(st.Files) > 0 && !agreed {
		if !terminal {
			return errors.New("the server holds this brain in the clear: run wong keys seal in a terminal to agree to replacing it, or pass --yes")
		}
		fmt.Fprintf(out, "The server holds the %d files of this brain in the clear. Sealing replaces them with encrypted\n"+
			"copies from this machine, which has them all, and deletes from the server their earlier versions.\n"+
			"Seal the brain? [y/N] ", len(st.Files))
		answer, _ := reader.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return errors.New("the brain was not sealed")
		}
	}
	key, err := seal.NewKey()
	if err != nil {
		return err
	}
	code := seal.NewRecovery()
	recovery, err := seal.RecoveryDevice(code)
	if err != nil {
		return err
	}
	if err := client.StartSeal(ctx, client.Device, recovery, key, st.Cursor); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(brain, ".wong", "sync.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	client.Key = key
	fmt.Fprintf(out, "\nThe brain is now end-to-end encrypted: the server cannot read it. Its recovery code:\n\n    %s\n\n"+
		"Write it down and keep it away from this machine. It opens the brain on a new machine if you lose\n"+
		"every one you have; without it and without them, nobody can open the brain, not even the server.\n", code)
	if !terminal {
		return nil
	}
	last := code[len(code)-4:]
	for {
		fmt.Fprint(out, "\nType its last four characters to confirm you wrote it down: ")
		line, err := reader.ReadString('\n')
		if strings.EqualFold(strings.TrimSpace(line), last) {
			return nil
		}
		if err != nil {
			return nil
		}
		fmt.Fprintln(out, "That is not how the code ends. Look at it again.")
	}
}

// waitForKey asks for the brain's key for this machine and waits until another approves it or the recovery code is
// typed instead.
func waitForKey(cmd *cobra.Command, client *remote.Client) error {
	in, ok := cmd.InOrStdin().(*os.File)
	if !ok || !isTerminal(in) {
		return remote.ErrLocked
	}
	ctx := cmd.Context()
	if err := client.RequestKey(ctx, client.Device); err != nil {
		return err
	}
	name, _ := os.Hostname()
	fmt.Fprintf(cmd.OutOrStdout(), "\nThe brain is end-to-end encrypted. To give this machine its key, run on a machine that has it\n\n"+
		"    wong keys approve %s\n\nand check it shows this code:\n\n    %s\n\nOr type the recovery code here.\n",
		name, seal.Fingerprint(client.Device.Public()))
	typed := typedLine(in)
	poll := time.NewTicker(3 * time.Second)
	defer poll.Stop()
	for {
		select {
		case line, ok := <-typed:
			if !ok {
				typed = nil
				continue
			}
			if err := recoverKey(ctx, client, line); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%v; type it again, or wait for the approval\n", err)
				typed = typedLine(in)
				continue
			}
			return nil
		case <-poll.C:
			keys, err := client.Keys(ctx)
			if err != nil {
				return err
			}
			if keys.Wrapped != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "approved: this machine has the brain's key")
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// recoverKey opens the brain's key with a recovery code and keeps it wrapped for this machine.
func recoverKey(ctx context.Context, client *remote.Client, code string) error {
	keys, err := client.Keys(ctx)
	if err != nil {
		return err
	}
	if !keys.Sealed {
		return errors.New("the brain is not end-to-end encrypted: there is nothing to recover")
	}
	recovery, err := seal.RecoveryDevice(code)
	if err != nil {
		return err
	}
	key, err := recovery.Unwrap(keys.Recovery)
	if err != nil {
		return errors.New("that recovery code does not open this brain")
	}
	if err := client.KeepOwnKey(ctx, client.Device, key); err != nil {
		return err
	}
	client.Key = key
	return nil
}

func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}
