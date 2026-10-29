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
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/dashboard"
	"github.com/17tayyy/wong/internal/mcp"
	"github.com/17tayyy/wong/internal/remote"
)

func newServeCmd() *cobra.Command {
	var db, dir, account, addr, newToken, revoke, certFile, keyFile string
	var list bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the wong server that keeps the brain for every machine",
		Long: "Run the wong server that keeps the brain for every machine.\n\n" +
			"Every file of the brain is stored with its history in a SQLite database. Each\n" +
			"machine uses its own token: create one with --new-token <name>, see them with\n" +
			"--tokens and revoke a lost machine's with --revoke-token <name>.\n\n" +
			"One server can keep a brain for each person or team: with --accounts <dir>, the\n" +
			"brain of each account is a SQLite file in that folder, and its tokens open it\n" +
			"alone, in the API, the dashboard and MCP. --account <id> --new-token <name>\n" +
			"creates the account with its first token; machines connect with it as to any\n" +
			"other server.\n\n" +
			"Webhooks leave tasks on the notes they make outdated: POST /hooks/github (push\n" +
			"events) and POST /hooks/linear (issue events), signed with the secret set on the\n" +
			"dashboard's Webhooks page, or in WONG_GITHUB_SECRET and WONG_LINEAR_SECRET. With\n" +
			"--accounts, each account sets its own, at /hooks/<account>/github and\n" +
			"/hooks/<account>/linear. Only /hooks/ needs to be reachable from the internet.\n" +
			"To use the API from outside your network, serve it over https: behind a tunnel or\n" +
			"a proxy with a certificate, or with --tls-cert and --tls-key.\n\n" +
			"The same address serves the dashboard: open it in a browser and sign in with a\n" +
			"token, for example one made with --new-token browser.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (certFile == "") != (keyFile == "") {
				return errors.New("--tls-cert and --tls-key go together")
			}
			if account != "" && dir == "" {
				return errors.New("--account goes with --accounts")
			}
			var brains remote.Brains
			var done bool
			if dir == "" {
				store, err := remote.Open(db)
				if err != nil {
					return err
				}
				defer store.Close()
				if done, err = tokenCmds(cmd, store, newToken, revoke, list); done || err != nil {
					return err
				}
				brains = store
			} else {
				folder, err := remote.OpenFolder(dir)
				if err != nil {
					return err
				}
				defer folder.Close()
				if done, err = folderTokenCmds(cmd, folder, account, newToken, revoke, list); done || err != nil {
					return err
				}
				brains = folder
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			hooks := remote.Webhooks{GitHubSecret: os.Getenv("WONG_GITHUB_SECRET"), LinearSecret: os.Getenv("WONG_LINEAR_SECRET")}
			api := remote.Handler(brains, log, hooks)
			devices := remote.NewDevices()
			web, err := dashboard.Handler(brains, log, hooks, devices)
			if err != nil {
				return err
			}
			root := http.NewServeMux()
			root.Handle("/api/", api)
			root.Handle("/api/device", remote.DeviceHandler(devices, log))
			root.Handle("/api/device/", remote.DeviceHandler(devices, log))
			root.Handle("/hooks/", api)
			root.Handle("/mcp", mcp.Handler(brains, log, version()))
			root.Handle("/", web)
			srv := &http.Server{
				Addr:              addr,
				Handler:           root,
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       2 * time.Minute,
				WriteTimeout:      2 * time.Minute,
				IdleTimeout:       2 * time.Minute,
			}
			go func() {
				<-ctx.Done()
				shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdown)
			}()
			where := []any{"db", db}
			if dir != "" {
				where = []any{"accounts", dir}
				if hooks != (remote.Webhooks{}) {
					log.Warn("WONG_GITHUB_SECRET and WONG_LINEAR_SECRET are left unused: with --accounts, each account sets its own on the dashboard's Webhooks page")
				}
			}
			log.Info("serving", append(where, "addr", addr, "tls", certFile != "")...)
			if certFile != "" {
				err = srv.ListenAndServeTLS(certFile, keyFile)
			} else {
				err = srv.ListenAndServe()
			}
			if !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&db, "db", "wong.db", "SQLite database file")
	cmd.Flags().StringVar(&dir, "accounts", "", "keep a brain for each account, one SQLite file each, in this folder")
	cmd.Flags().StringVar(&account, "account", "", "with --accounts: the account whose tokens --new-token, --tokens and --revoke-token act on")
	cmd.Flags().StringVar(&addr, "addr", ":8787", "address to listen on")
	cmd.Flags().StringVar(&newToken, "new-token", "", "create a token for this machine name, print it and exit")
	cmd.Flags().BoolVar(&list, "tokens", false, "list who has a token and when they last used it, and exit")
	cmd.Flags().StringVar(&revoke, "revoke-token", "", "revoke this machine name's token and exit")
	cmd.Flags().StringVar(&certFile, "tls-cert", "", "serve https with this certificate file")
	cmd.Flags().StringVar(&keyFile, "tls-key", "", "and this key file")
	cmd.MarkFlagsMutuallyExclusive("new-token", "tokens", "revoke-token")
	cmd.MarkFlagsMutuallyExclusive("db", "accounts")
	return cmd
}

// tokenCmds runs --new-token, --revoke-token or --tokens on a brain, and reports whether one was asked for.
func tokenCmds(cmd *cobra.Command, store *remote.Store, newToken, revoke string, list bool) (bool, error) {
	out := cmd.OutOrStdout()
	switch {
	case newToken != "":
		token, err := store.NewToken(newToken)
		if err != nil {
			return true, err
		}
		fmt.Fprintln(out, token)
	case revoke != "":
		found, err := store.RevokeToken(revoke)
		if err != nil {
			return true, err
		}
		if !found {
			return true, fmt.Errorf("no token named %q", revoke)
		}
		fmt.Fprintf(out, "revoked the token of %s\n", revoke)
	case list:
		tokens, err := store.Tokens()
		if err != nil {
			return true, err
		}
		return true, printTokens(out, tokens)
	default:
		return false, nil
	}
	return true, nil
}

// folderTokenCmds is tokenCmds on an account of a folder: --new-token creates the account, and --tokens lists every account's without --account.
func folderTokenCmds(cmd *cobra.Command, folder *remote.Folder, account, newToken, revoke string, list bool) (bool, error) {
	switch {
	case (newToken != "" || revoke != "") && account == "":
		return true, errors.New("with --accounts, say which account with --account <id>")
	case newToken != "":
		store, created, err := folder.Create(account)
		if err != nil {
			return true, err
		}
		if created {
			fmt.Fprintf(cmd.ErrOrStderr(), "created the account %s\n", account)
		}
		return tokenCmds(cmd, store, newToken, "", false)
	case revoke != "":
		store, err := folder.Store(account)
		if err != nil {
			return true, err
		}
		return tokenCmds(cmd, store, "", revoke, false)
	case list:
		ids := []string{account}
		if account == "" {
			var err error
			if ids, err = folder.IDs(); err != nil {
				return true, err
			}
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ACCOUNT\tNAME\tCREATED\tLAST USED")
		for _, id := range ids {
			store, err := folder.Store(id)
			if err != nil {
				return true, err
			}
			tokens, err := store.Tokens()
			if err != nil {
				return true, err
			}
			for _, t := range tokens {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", id, t.Name, t.Created, lastUsed(t))
			}
		}
		return true, w.Flush()
	}
	return false, nil
}

func printTokens(out io.Writer, tokens []remote.TokenInfo) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCREATED\tLAST USED")
	for _, t := range tokens {
		fmt.Fprintf(w, "%s\t%s\t%s\n", t.Name, t.Created, lastUsed(t))
	}
	return w.Flush()
}

func lastUsed(t remote.TokenInfo) string {
	if t.LastUsed == "" {
		return "never"
	}
	return t.LastUsed
}

func newRemoteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remote <url>",
		Short: "Connect this machine to a wong server, approved in a browser or with a token on stdin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := setRemote(cmd, args[0])
			return err
		},
	}
}

// setRemote saves the server at url with this machine's token: read from stdin, or on a terminal, pasted or got by approving the machine in a browser.
func setRemote(cmd *cobra.Command, url string) (*remote.Client, error) {
	if err := remote.CheckURL(url); err != nil {
		return nil, err
	}
	var token string
	if f, ok := cmd.InOrStdin().(*os.File); ok && isTerminal(f) {
		name, _ := os.Hostname()
		var err error
		if token, err = approvedToken(cmd.Context(), remote.NewClient(url, "", 30*time.Second), name, cmd.ErrOrStderr(), typedLine(f)); err != nil {
			return nil, err
		}
	} else {
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if token = strings.TrimSpace(line); token == "" {
			return nil, errors.New("pass the token on stdin, e.g. wong remote <url> < token.txt")
		}
	}
	path, err := remote.ConfigPath()
	if err != nil {
		return nil, err
	}
	if err := remote.SaveConfig(path, remote.Config{URL: url, Token: token}); err != nil {
		return nil, err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "server set in %s\n", path)
	return remote.NewClient(url, token, 30*time.Second), nil
}

// approvedToken asks the server to let this machine in once someone signed in to the dashboard enters the code it shows,
// and waits for that or for a token typed instead; with a server that cannot do that, it waits for the token alone.
func approvedToken(ctx context.Context, client *remote.Client, name string, out io.Writer, typed <-chan string) (string, error) {
	start, err := client.AskDevice(ctx, name)
	if errors.Is(err, remote.ErrNoDevices) {
		fmt.Fprint(out, "Token for this machine (on the server: wong serve --new-token <name>): ")
		select {
		case token, ok := <-typed:
			if !ok {
				return "", errors.New("no token was entered")
			}
			return token, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err != nil {
		return "", err
	}
	fmt.Fprintf(out, "To connect this machine, open %s in a browser signed in to wong and enter\n\n    %s\n\n"+
		"or paste a token made on the server with wong serve --new-token <name>.\n", start.URL, start.Code)
	poll := time.NewTicker(time.Duration(start.Interval) * time.Second)
	defer poll.Stop()
	expired := time.After(time.Duration(start.ExpiresIn) * time.Second)
	for {
		select {
		case token, ok := <-typed:
			if ok {
				return token, nil
			}
			typed = nil
		case <-poll.C:
			status, got, err := client.PollDevice(ctx, start.Secret)
			switch {
			case err != nil:
				return "", err
			case status == remote.DeviceApproved && got.Account != "":
				fmt.Fprintf(out, "approved: this machine is %s in the account %s\n", got.Name, got.Account)
				return got.Token, nil
			case status == remote.DeviceApproved:
				fmt.Fprintf(out, "approved: this machine is %s\n", got.Name)
				return got.Token, nil
			case status == remote.DeviceDenied:
				return "", errors.New("the machine was turned away in the dashboard")
			case status == remote.DeviceGone:
				return "", errors.New("the code expired: run the command again for a new one")
			}
		case <-expired:
			return "", errors.New("the code expired: run the command again for a new one")
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// typedLine hands over the first line typed that is not blank, and closes when the input ends without one.
func typedLine(in io.Reader) <-chan string {
	lines := make(chan string, 1)
	go func() {
		defer close(lines)
		scan := bufio.NewScanner(in)
		for scan.Scan() {
			if line := strings.TrimSpace(scan.Text()); line != "" {
				lines <- line
				return
			}
		}
	}()
	return lines
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// syncBrain pulls from and pushes to the server, as asked, and lists the conflicts left.
func syncBrain(cmd *cobra.Command, client *remote.Client, brain string, pull, push bool) error {
	out := cmd.OutOrStdout()
	if pull {
		res, err := remote.Pull(cmd.Context(), client, brain)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "pulled: %d updated, %d deleted\n", len(res.Updated), len(res.Deleted))
	}
	if push {
		res, err := remote.Push(cmd.Context(), client, brain)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "pushed: %d\n", len(res.Pushed))
	}
	st, err := remote.LoadState(brain)
	if err != nil {
		return err
	}
	if len(st.Conflicts) > 0 {
		fmt.Fprintf(out, "%d files changed both here and on the server; compare with %s and run wong resolve:\n", len(st.Conflicts), filepath.Join(brain, ".wong", "conflicts"))
		for path := range st.Conflicts {
			fmt.Fprintln(out, "  ", path)
		}
	}
	return nil
}

func newSyncCmds(brain *string) []*cobra.Command {
	run := func(pull, push bool) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			client, err := requireRemote(30 * time.Second)
			if err != nil {
				return err
			}
			return syncBrain(cmd, client, *brain, pull, push)
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
