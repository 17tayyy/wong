package main

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/mcp"
	"github.com/17tayyy/wong/internal/remote"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the brain over MCP on stdin and stdout, through this machine's wong server",
		Long: "Serve the brain over MCP on stdin and stdout, for agents that start their MCP servers as a\n" +
			"command, such as Codex. It relays every message to the /mcp endpoint of the server set with\n" +
			"wong remote, with this machine's token, so the agent's changes are signed by this machine.\n\n" +
			"Clients that speak Streamable HTTP can use <server>/mcp directly, with a token of their own:\n" +
			"wong serve --new-token <name> on the server.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := remote.ConfigPath()
			if err != nil {
				return err
			}
			cfg, ok, err := remote.LoadConfig(path)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("no server set: run wong remote <url>")
			}
			client := &http.Client{Timeout: 2 * time.Minute}
			return mcp.Bridge(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), strings.TrimRight(cfg.URL, "/")+"/mcp", cfg.Token, client)
		},
	}
}
