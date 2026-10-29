package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
)

func newOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open",
		Short: "Open the dashboard in a browser, signed in to this machine's brain",
		Long: "Open the dashboard in a browser, signed in to this machine's brain.\n\n" +
			"The server makes a link that works once, for two minutes; wong opens it, and\n" +
			"prints it for a browser on another device.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := requireRemote(30 * time.Second)
			if err != nil {
				return err
			}
			link, err := client.SignIn(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\nworks once, for %d minutes\n", link.URL, link.ExpiresIn/60)
			if err := openBrowser(link.URL); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "could not open a browser here: open the link above")
			}
			return nil
		},
	}
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
