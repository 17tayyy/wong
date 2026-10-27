package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/remote"
)

func newServeCmd() *cobra.Command {
	var db, addr, newToken string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the wong server that keeps the brain for every machine",
		Long: "Run the wong server that keeps the brain for every machine.\n\n" +
			"Every file of the brain is stored with its history in a SQLite database. Each\n" +
			"machine uses its own token: create one with --new-token <name>.\n\n" +
			"Webhooks leave tasks on the notes they make outdated: POST /hooks/github (push\n" +
			"events) with WONG_GITHUB_SECRET set, and POST /hooks/linear (issue events) with\n" +
			"WONG_LINEAR_SECRET set. Only those paths need to be reachable from the internet.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := remote.Open(db)
			if err != nil {
				return err
			}
			defer store.Close()
			if newToken != "" {
				token, err := store.NewToken(newToken)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), token)
				return nil
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			hooks := remote.Webhooks{GitHubSecret: os.Getenv("WONG_GITHUB_SECRET"), LinearSecret: os.Getenv("WONG_LINEAR_SECRET")}
			srv := &http.Server{Addr: addr, Handler: remote.Handler(store, log, hooks), ReadHeaderTimeout: 10 * time.Second}
			go func() {
				<-ctx.Done()
				shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdown)
			}()
			log.Info("serving", "addr", addr, "db", db, "github_hook", hooks.GitHubSecret != "", "linear_hook", hooks.LinearSecret != "")
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&db, "db", "wong.db", "SQLite database file")
	cmd.Flags().StringVar(&addr, "addr", ":8787", "address to listen on")
	cmd.Flags().StringVar(&newToken, "new-token", "", "create a token for this machine name, print it and exit")
	return cmd
}

func newRemoteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remote <url>",
		Short: "Set this machine's wong server; the token is read from stdin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			token := strings.TrimSpace(line)
			if token == "" {
				return errors.New("pass the token on stdin, e.g. wong remote <url> < token.txt")
			}
			path, err := remote.ConfigPath()
			if err != nil {
				return err
			}
			if err := remote.SaveConfig(path, remote.Config{URL: args[0], Token: token}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "server set in %s\n", path)
			return nil
		},
	}
}

func newSyncCmds(brain *string) []*cobra.Command {
	run := func(pull, push bool) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			client, err := requireRemote(30 * time.Second)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if pull {
				res, err := remote.Pull(cmd.Context(), client, *brain)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "pulled: %d updated, %d deleted\n", len(res.Updated), len(res.Deleted))
			}
			if push {
				res, err := remote.Push(cmd.Context(), client, *brain)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "pushed: %d\n", len(res.Pushed))
			}
			st, err := remote.LoadState(*brain)
			if err != nil {
				return err
			}
			if len(st.Conflicts) > 0 {
				fmt.Fprintf(out, "%d files changed both here and on the server; compare with %s and run wong resolve:\n", len(st.Conflicts), filepath.Join(*brain, ".wong", "conflicts"))
				for path := range st.Conflicts {
					fmt.Fprintln(out, "  ", path)
				}
			}
			return nil
		}
	}
	var keep string
	resolve := &cobra.Command{
		Use:   "resolve <path> --keep local|server",
		Short: "End a sync conflict keeping this machine's version or the server's",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if keep != "local" && keep != "server" {
				return errors.New("--keep must be local or server")
			}
			if err := remote.Resolve(*brain, args[0], keep == "local"); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "kept the %s version of %s; it syncs on the next push\n", keep, args[0])
			return nil
		},
	}
	resolve.Flags().StringVar(&keep, "keep", "", "local or server")
	_ = resolve.MarkFlagRequired("keep")
	return []*cobra.Command{
		{Use: "pull", Short: "Bring the server's changes into the brain", Args: cobra.NoArgs, RunE: run(true, false)},
		{Use: "push", Short: "Send the brain's changes to the server", Args: cobra.NoArgs, RunE: run(false, true)},
		{Use: "sync", Short: "Pull, then push", Args: cobra.NoArgs, RunE: run(true, true)},
		resolve,
	}
}

// remoteClient returns a client for this machine's server, or nil when none is set.
func remoteClient(timeout time.Duration) (*remote.Client, error) {
	path, err := remote.ConfigPath()
	if err != nil {
		return nil, err
	}
	cfg, ok, err := remote.LoadConfig(path)
	if err != nil || !ok {
		return nil, err
	}
	return remote.NewClient(cfg.URL, cfg.Token, timeout), nil
}

// requireRemote is remoteClient for the commands that need a server.
func requireRemote(timeout time.Duration) (*remote.Client, error) {
	client, err := remoteClient(timeout)
	if err == nil && client == nil {
		err = errors.New("no server set: run wong remote <url>")
	}
	return client, err
}
