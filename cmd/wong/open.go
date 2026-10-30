package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/local"
	"github.com/17tayyy/wong/internal/remote"
)

func newOpenCmd(brain *string) *cobra.Command {
	return &cobra.Command{
		Use:   "open",
		Short: "Open the dashboard in a browser, signed in to this machine's brain",
		Long: "Open the dashboard in a browser, signed in to this machine's brain.\n\n" +
			"The server makes a link that works once, for two minutes; wong opens it, and\n" +
			"prints it for a browser on another device.\n\n" +
			"The server cannot read an end-to-end encrypted brain, so for one wong serves the\n" +
			"dashboard here, on this machine, from the decrypted copy, until you stop it; what\n" +
			"you change there reaches the server encrypted, like any other change.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := requireRemote(30 * time.Second)
			if err != nil {
				return err
			}
			if err := client.Unlock(cmd.Context()); err != nil {
				return err
			}
			if client.Key != nil {
				return openLocal(cmd, client, *brain)
			}
			return openLink(cmd, client, "")
		},
	}
}

// openLocal opens the dashboard of the copy another wong process serves for this brain, or serves it until stopped.
func openLocal(cmd *cobra.Command, cloud *remote.Client, brain string) error {
	if url, token, ok := local.Find(brain); ok {
		return openLink(cmd, remote.NewClient(url, token, 10*time.Second), "")
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	m, err := local.Start(brain, cloud, version(), log)
	if err != nil {
		return err
	}
	if err := m.Sync(ctx); err != nil {
		m.Close()
		return err
	}
	note := "The brain is end-to-end encrypted, so its dashboard runs on this machine, at " + m.URL +
		". Keep this running while you use it; Ctrl-C stops it.\n"
	if err := openLink(cmd, remote.NewClient(m.URL, m.Token, 10*time.Second), note); err != nil {
		m.Close()
		return err
	}
	return m.Run(ctx, 3*time.Second)
}

func openLink(cmd *cobra.Command, client *remote.Client, note string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	link, err := client.SignIn(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s%s\nworks once, for %d minutes\n", note, link.URL, link.ExpiresIn/60)
	if err := openBrowser(link.URL); err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "could not open a browser here: open the link above")
	}
	return nil
}

func openBrowser(url string) error {
	switch {
	case runtime.GOOS == "darwin":
		return exec.Command("open", url).Start()
	case runtime.GOOS == "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case os.Getenv("WSL_DISTRO_NAME") != "":
		return exec.Command("explorer.exe", url).Start()
	}
	return exec.Command("xdg-open", url).Start()
}
