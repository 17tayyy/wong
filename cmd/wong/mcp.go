package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/local"
	"github.com/17tayyy/wong/internal/mcp"
	"github.com/17tayyy/wong/internal/remote"
)

func newMCPCmd(brain *string) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the brain over MCP on stdin and stdout, through this machine's wong server",
		Long: "Serve the brain over MCP on stdin and stdout, for agents that start their MCP servers as a\n" +
			"command, such as Codex. It relays every message to the /mcp endpoint of the server set with\n" +
			"wong remote, with this machine's token, so the agent's changes are signed by this machine.\n\n" +
			"Clients that speak Streamable HTTP can use <server>/mcp directly, with a token of their own:\n" +
			"wong serve --new-token <name> on the server.\n\n" +
			"The server cannot read an end-to-end encrypted brain, so for one the messages go to the\n" +
			"decrypted copy on this machine instead: the one wong open serves, or one served for as long\n" +
			"as the agent keeps this running.",
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
			cloud, err := remoteClient(30 * time.Second)
			if err != nil {
				return err
			}
			if err := cloud.Unlock(cmd.Context()); err != nil {
				return err
			}
			if cloud.Key == nil {
				return mcp.Bridge(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), strings.TrimRight(cfg.URL, "/")+"/mcp", cfg.Token, client)
			}
			if url, token, ok := local.Find(*brain); ok {
				return mcp.Bridge(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), url+"/mcp", token, client)
			}
			m, err := local.Start(*brain, cloud, version(), slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn})))
			if err != nil {
				return err
			}
			ctx, stop := context.WithCancel(cmd.Context())
			done := make(chan error, 1)
			go func() { done <- m.Run(ctx, 5*time.Second) }()
			err = mcp.Bridge(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), m.URL+"/mcp", m.Token, client)
			stop()
			return errors.Join(err, <-done)
		},
	}
}
