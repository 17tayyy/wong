package main

import (
	"bufio"
	"cmp"
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
	"golang.org/x/term"

	"github.com/17tayyy/wong/internal/dashboard"
	"github.com/17tayyy/wong/internal/linear"
	"github.com/17tayyy/wong/internal/mcp"
	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/ui"
)

func newServeCmd() *cobra.Command {
	var db, dir, account, addr, certFile, keyFile, signInURL, accountService, statusURL, maxSize, maxFile, githubApp string
	var sealedOnly bool
	var maxRequests int
	var admin adminFlags
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
			"/hooks/<account>/linear. --hook-secret github|linear saves one read from stdin.\n" +
			"Only /hooks/ needs to be reachable from the internet.\n\n" +
			"With --github-app or WONG_GITHUB_APP set to the file wong github-app writes, people\n" +
			"connect GitHub from the Webhooks page in one go: they install the app on their account\n" +
			"or organization and pick the repos, and the app's webhooks, at POST /hooks/github-app,\n" +
			"reach every brain linked to that installation. wong also checks each pull request: how\n" +
			"many notes cite files it removes or moves, and how many pitfalls are noted on files it\n" +
			"changes, never which. On a sealed brain the check waits for one of its machines, which\n" +
			"can read the notes.\n\n" +
			"With WONG_LINEAR_CLIENT_ID and WONG_LINEAR_WEBHOOK_SECRET, from a Linear OAuth\n" +
			"application whose callback URL is the server's /webhooks/linear/callback and whose\n" +
			"webhook, on issues, is its /hooks/linear-app, people connect Linear from the Webhooks\n" +
			"page: the issues that close in their workspace reach their brain, and their machines\n" +
			"get Linear's token, sealed for them on an encrypted brain, to check the issues notes\n" +
			"cite. The application is used without its secret: PKCE stands for it, so the\n" +
			"machines renew the token themselves; on an encrypted brain the server cannot read it.\n\n" +
			"To use the API from outside your network, serve it over https: behind a tunnel or\n" +
			"a proxy with a certificate, or with --tls-cert and --tls-key.\n\n" +
			"The same address serves the dashboard. wong open, on a connected machine, opens it\n" +
			"signed in; a token, such as one made with --new-token browser, signs in too.\n\n" +
			"With --accounts and WONG_ADMIN_TOKEN set, a sign-up service can create accounts\n" +
			"and sign people in to them: PUT /admin/accounts/<id>, and POST\n" +
			"/admin/accounts/<id>/signin for a one-time link, with that token as bearer;\n" +
			"GET /admin/accounts lists every account with its size and last change.\n" +
			"--sign-in-url sends people without a token to the service's sign-in page, and\n" +
			"--account-service adds an Account page to the dashboard, filled with what the\n" +
			"service answers at its URL, with {account} replaced, asked with that token.\n" +
			"--status-url links the dashboard to a page that says whether the service works.\n\n" +
			"MCP clients such as claude.ai connect with OAuth: given the address of /mcp, they\n" +
			"register and send you to approve them on the dashboard, and get a token of the\n" +
			"brain, named after them and revoked like any other. The key that signs their\n" +
			"registrations is kept beside the database, or in the accounts folder.\n\n" +
			"--max-size, --max-file and --max-requests bound what each brain may take: its\n" +
			"size on disk with its history, the size of one file, and API requests a minute.\n" +
			"--sealed keeps every brain end-to-end encrypted: a brain takes no file until a\n" +
			"machine seals it, and the server never holds one it can read.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (certFile == "") != (keyFile == "") {
				return errors.New("--tls-cert and --tls-key go together")
			}
			if account != "" && dir == "" {
				return errors.New("--account goes with --accounts")
			}
			limits := remote.Limits{Requests: maxRequests, Sealed: sealedOnly}
			for _, l := range []struct {
				flag, value string
				to          *int64
			}{{"--max-size", maxSize, &limits.Size}, {"--max-file", maxFile, &limits.File}} {
				if l.value == "" {
					continue
				}
				n, err := remote.ParseBytes(l.value)
				if err != nil {
					return fmt.Errorf("%s: %w", l.flag, err)
				}
				*l.to = n
			}
			var brains remote.Brains
			var done bool
			if dir == "" {
				store, err := remote.Open(db)
				if err != nil {
					return err
				}
				defer store.Close()
				if done, err = adminCmds(cmd, store, admin); done || err != nil {
					return err
				}
				store.SetLimits(limits)
				cloud, err := remote.NewCloudAIs(db + ".cloud-key")
				if err != nil {
					return err
				}
				store.SetCloudAIs(cloud)
				brains = store
			} else {
				folder, err := remote.OpenFolder(dir)
				if err != nil {
					return err
				}
				defer folder.Close()
				if done, err = folderAdminCmds(cmd, folder, account, admin); done || err != nil {
					return err
				}
				folder.SetLimits(limits)
				cloud, err := remote.NewCloudAIs(filepath.Join(dir, ".cloud-key"))
				if err != nil {
					return err
				}
				folder.SetCloudAIs(cloud)
				brains = folder
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			hooks := remote.Webhooks{GitHubSecret: os.Getenv("WONG_GITHUB_SECRET"), LinearSecret: os.Getenv("WONG_LINEAR_SECRET")}
			linksFile := db + ".links.json"
			if dir != "" {
				linksFile = filepath.Join(dir, ".links.json")
			}
			links, err := remote.OpenLinks(linksFile)
			if err != nil {
				return err
			}
			hooks.Links = links
			if folder, ok := brains.(*remote.Folder); ok {
				folder.SetLinks(links)
			}
			if githubApp = cmp.Or(githubApp, os.Getenv("WONG_GITHUB_APP")); githubApp != "" {
				if hooks.App, err = remote.LoadGitHubApp(githubApp); err != nil {
					return err
				}
			}
			if id := os.Getenv("WONG_LINEAR_CLIENT_ID"); id != "" {
				secret := os.Getenv("WONG_LINEAR_WEBHOOK_SECRET")
				if secret == "" {
					return errors.New("WONG_LINEAR_CLIENT_ID goes with WONG_LINEAR_WEBHOOK_SECRET, the secret that signs the Linear application's webhooks")
				}
				hooks.Linear = &remote.LinearApp{OAuth: linear.OAuth{ClientID: id}, WebhookSecret: secret}
			}
			api := remote.Handler(brains, log, hooks)
			devices, signins := remote.NewDevices(), remote.NewSignIns()
			oauthKey := db + ".oauth-key"
			if dir != "" {
				oauthKey = filepath.Join(dir, ".oauth-key")
			}
			oauth, err := remote.NewOAuth(oauthKey)
			if err != nil {
				return err
			}
			opts := dashboard.Options{Webhooks: hooks, Devices: devices, SignIns: signins, OAuth: oauth, SignInURL: signInURL, StatusURL: statusURL}
			if accountService != "" {
				if dir == "" || os.Getenv("WONG_ADMIN_TOKEN") == "" || !strings.Contains(accountService, "{account}") {
					return errors.New("--account-service needs --accounts, WONG_ADMIN_TOKEN and {account} in its URL")
				}
				opts.Account = &dashboard.AccountService{URL: accountService, Token: os.Getenv("WONG_ADMIN_TOKEN")}
			}
			web, err := dashboard.Handler(brains, log, opts)
			if err != nil {
				return err
			}
			root := http.NewServeMux()
			root.Handle("/api/", api)
			root.Handle("/api/device", remote.DeviceHandler(devices, log))
			root.Handle("/api/device/", remote.DeviceHandler(devices, log))
			root.Handle("/api/signin", remote.SignInHandler(brains, signins, log))
			connect := remote.OAuthHandler(oauth, log)
			root.Handle("/.well-known/", connect)
			root.Handle("/oauth/register", connect)
			root.Handle("/oauth/token", connect)
			if folder, ok := brains.(*remote.Folder); ok {
				root.Handle("/admin/", remote.AdminHandler(folder, signins, os.Getenv("WONG_ADMIN_TOKEN"), log))
			}
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
			where := []any{"accounts", dir}
			if store, ok := brains.(*remote.Store); ok {
				where = []any{"db", db}
				env := map[string]string{"github": hooks.GitHubSecret, "linear": hooks.LinearSecret}
				for _, hook := range remote.Hooks {
					secret, _ := store.HookSecret(hook)
					where = append(where, hook+"_hook", env[hook] != "" || secret != "")
				}
			} else {
				where = append(where, "admin", os.Getenv("WONG_ADMIN_TOKEN") != "")
			}
			if _, ok := brains.(*remote.Folder); ok && (hooks.GitHubSecret != "" || hooks.LinearSecret != "") {
				log.Warn("WONG_GITHUB_SECRET and WONG_LINEAR_SECRET are left unused: with --accounts, each account sets its own on the dashboard's Webhooks page")
			}
			if hooks.App != nil {
				where = append(where, "github_app", hooks.App.Slug)
			}
			if hooks.Linear != nil {
				where = append(where, "linear_app", true)
			}
			if limits != (remote.Limits{}) {
				where = append(where, "max_size", limits.Size, "max_file", limits.File, "max_requests", limits.Requests)
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
	cmd.Flags().StringVar(&maxSize, "max-size", "", "the most room each brain may take on disk, history included, such as 100M")
	cmd.Flags().BoolVar(&sealedOnly, "sealed", false, "keep every brain end-to-end encrypted: a brain takes no file until a machine seals it")
	cmd.Flags().StringVar(&maxFile, "max-file", "", "the largest file a brain may hold, such as 2M")
	cmd.Flags().IntVar(&maxRequests, "max-requests", 0, "API requests a minute each brain may make, in bursts of as many")
	cmd.Flags().StringVar(&signInURL, "sign-in-url", "", "send people without a token to this sign-in page, such as a sign-up service's")
	cmd.Flags().StringVar(&githubApp, "github-app", "", "take pushes and pull requests from the GitHub App whose keys wong github-app wrote to this file (or WONG_GITHUB_APP)")
	cmd.Flags().StringVar(&statusURL, "status-url", "", "link the dashboard to this page that says whether the service works")
	cmd.Flags().StringVar(&accountService, "account-service", "", "with --accounts: ask this sign-up service's URL, with {account} in it, what each account's plan is, for the Account page")
	cmd.Flags().StringVar(&admin.newToken, "new-token", "", "create a token for this machine name, print it and exit")
	cmd.Flags().BoolVar(&admin.list, "tokens", false, "list who has a token and when they last used it, and exit")
	cmd.Flags().StringVar(&admin.revoke, "revoke-token", "", "revoke this machine name's token and exit")
	cmd.Flags().StringVar(&admin.hook, "hook-secret", "", "save the secret of this webhook, github or linear, read from stdin, and exit; an empty one turns it off")
	cmd.Flags().StringVar(&certFile, "tls-cert", "", "serve https with this certificate file")
	cmd.Flags().StringVar(&keyFile, "tls-key", "", "and this key file")
	cmd.MarkFlagsMutuallyExclusive("new-token", "tokens", "revoke-token", "hook-secret")
	cmd.MarkFlagsMutuallyExclusive("db", "accounts")
	return cmd
}

// adminFlags are the flags of serve that change a brain or list its tokens, and exit instead of serving.
type adminFlags struct {
	newToken, revoke, hook string
	list                   bool
}

// adminCmds runs the admin flag given on a brain, and reports whether there was one.
func adminCmds(cmd *cobra.Command, store *remote.Store, a adminFlags) (bool, error) {
	out := cmd.OutOrStdout()
	switch {
	case a.newToken != "":
		token, err := store.NewToken(a.newToken)
		if err != nil {
			return true, err
		}
		fmt.Fprintln(out, token)
	case a.revoke != "":
		found, err := store.RevokeToken(a.revoke)
		if err != nil {
			return true, err
		}
		if !found {
			return true, fmt.Errorf("no token named %q", a.revoke)
		}
		fmt.Fprintf(out, "revoked the token of %s\n", a.revoke)
	case a.hook != "":
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return true, err
		}
		secret := strings.TrimSpace(line)
		if err := store.SetHookSecret(a.hook, secret); err != nil {
			return true, err
		}
		if secret == "" {
			fmt.Fprintf(out, "turned the %s webhook off\n", a.hook)
		} else {
			fmt.Fprintf(out, "saved the secret of the %s webhook\n", a.hook)
		}
	case a.list:
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

// folderAdminCmds is adminCmds on an account of a folder: --new-token creates the account, and --tokens lists every account's without --account.
func folderAdminCmds(cmd *cobra.Command, folder *remote.Folder, account string, a adminFlags) (bool, error) {
	switch {
	case (a.newToken != "" || a.revoke != "" || a.hook != "") && account == "":
		return true, errors.New("with --accounts, say which account with --account <id>")
	case a.newToken != "":
		store, created, err := folder.Create(account)
		if err != nil {
			return true, err
		}
		if created {
			fmt.Fprintf(cmd.ErrOrStderr(), "created the account %s\n", account)
		}
		return adminCmds(cmd, store, a)
	case a.revoke != "" || a.hook != "":
		store, err := folder.Store(account)
		if err != nil {
			return true, err
		}
		return adminCmds(cmd, store, a)
	case a.list:
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
			u := ui.New(cmd.OutOrStdout(), cmd.InOrStdin())
			defer u.Close()
			_, err := setRemote(cmd, u, args[0])
			return err
		},
	}
}

// setRemote saves the server at url with this machine's token: read from stdin, or on a terminal, pasted or got by approving the machine in a browser.
func setRemote(cmd *cobra.Command, u *ui.UI, url string) (*remote.Client, error) {
	if err := remote.CheckURL(url); err != nil {
		return nil, err
	}
	var token string
	name, _ := os.Hostname()
	probe := remote.NewClient(url, "", 30*time.Second)
	f, ok := cmd.InOrStdin().(*os.File)
	switch {
	case u.CanAsk():
		var err error
		if token, err = approveInFrame(cmd.Context(), u, probe, name); err != nil {
			return nil, err
		}
	case ok && isTerminal(f) && !ui.UnderAgent():
		var err error
		if token, err = approvedToken(cmd.Context(), probe, name, cmd.ErrOrStderr(), typedLine(f)); err != nil {
			return nil, err
		}
	default:
		typed := typedLine(cmd.InOrStdin())
		select {
		case line, ok := <-typed:
			token = line
			if !ok {
				typed = nil
			}
		case <-time.After(2 * time.Second):
		}
		if token == "" {
			var err error
			if token, err = approvedToken(cmd.Context(), probe, name, cmd.OutOrStdout(), typed); err != nil {
				return nil, err
			}
		}
	}
	path, err := remote.ConfigPath()
	if err != nil {
		return nil, err
	}
	if err := remote.SaveConfig(path, remote.Config{URL: url, Token: token}); err != nil {
		return nil, err
	}
	if !u.Fancy() {
		fmt.Fprintf(cmd.OutOrStdout(), "server set in %s\n", path)
	}
	return remote.NewClient(url, token, 30*time.Second), nil
}

// approveInFrame shows the code to enter in the dashboard and waits for the approval, opening the page on Enter
// and taking a pasted token on t; with a server that cannot do that, it asks for the token alone.
func approveInFrame(ctx context.Context, u *ui.UI, client *remote.Client, name string) (string, error) {
	tokenPrompt := ui.Text{Question: "Token for this machine", Hint: "Made on the server with wong serve --new-token <name>", Secret: true, Check: filled}
	sp := u.Spin("Asking "+client.URL+" to let this machine in", "")
	start, err := client.AskDevice(ctx, name)
	sp.Stop("")
	if errors.Is(err, remote.ErrNoDevices) {
		return u.Ask(tokenPrompt)
	}
	if err != nil {
		return "", err
	}
	u.Note("Connect this machine",
		"Open "+u.Accent(start.URL)+" in a browser signed in to wong, and enter this code:", "", "    "+u.Bold(u.Accent(start.Code)))
	const hint = "enter: open it in your browser · t: paste a token instead"
	sp = u.Spin("Waiting for the approval", hint)
	poll := time.NewTicker(time.Duration(start.Interval) * time.Second)
	defer poll.Stop()
	expired := time.After(time.Duration(start.ExpiresIn) * time.Second)
	for {
		select {
		case k, ok := <-u.Keys():
			switch {
			case !ok:
				sp.Stop("")
				return "", ui.ErrCancelled
			case k.Name == "enter":
				if openBrowser(start.URL) == nil {
					sp.Hint("opened in your browser · t: paste a token instead")
				}
			case k.Rune == 't':
				sp.Stop("")
				tokenPrompt.Hint = "Made on the server with wong serve --new-token <name> · Esc: back to waiting"
				token, err := u.Ask(tokenPrompt)
				if !errors.Is(err, ui.ErrCancelled) {
					return token, err
				}
				sp = u.Spin("Waiting for the approval", hint)
			}
		case <-poll.C:
			status, got, err := client.PollDevice(ctx, start.Secret)
			switch {
			case err != nil:
				sp.Stop("")
				return "", err
			case status == remote.DeviceApproved && got.Account != "":
				sp.Stop("Approved: this machine is " + u.Bold(got.Name) + " in the account " + u.Bold(got.Account))
				return got.Token, nil
			case status == remote.DeviceApproved:
				sp.Stop("Approved: this machine is " + u.Bold(got.Name))
				return got.Token, nil
			case status == remote.DeviceDenied:
				sp.Stop("")
				return "", errors.New("the machine was turned away in the dashboard")
			case status == remote.DeviceGone:
				sp.Stop("")
				return "", errors.New("the code expired: run the command again for a new one")
			}
		case <-expired:
			sp.Stop("")
			return "", errors.New("the code expired: run the command again for a new one")
		case <-ctx.Done():
			sp.Stop("")
			return "", ctx.Err()
		}
	}
}

func filled(s string) error {
	if s == "" {
		return errors.New("type or paste it, or press Esc")
	}
	return nil
}

// approvedToken asks the server to let this machine in once someone signed in to the dashboard enters the code it shows,
// and waits for that or for a token typed instead; with a server that cannot do that, it waits for the token alone.
// With no input to type into, typed is nil and it only waits, as when an agent runs it.
func approvedToken(ctx context.Context, client *remote.Client, name string, out io.Writer, typed <-chan string) (string, error) {
	start, err := client.AskDevice(ctx, name)
	if errors.Is(err, remote.ErrNoDevices) && typed == nil {
		return "", errors.New("this server lets machines in with a token only: pass one on stdin, e.g. wong setup <url> < token.txt")
	}
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
	if typed == nil {
		fmt.Fprintf(out, "To connect this machine, open %s in a browser signed in to wong and enter\n\n    %s\n\n"+
			"wong waits here until the machine is approved, for up to %d minutes.\n", start.URL, start.Code, max(start.ExpiresIn/60, 1))
	} else {
		fmt.Fprintf(out, "To connect this machine, open %s in a browser signed in to wong and enter\n\n    %s\n\n"+
			"or paste a token made on the server with wong serve --new-token <name>.\n", start.URL, start.Code)
	}
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
	return term.IsTerminal(int(f.Fd()))
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
		if len(res.Refused) > 0 {
			if len(res.Refused) == 1 {
				fmt.Fprintln(out, "1 file was not pushed; fix it and push again:")
			} else {
				fmt.Fprintf(out, "%d files were not pushed; fix them and push again:\n", len(res.Refused))
			}
			for _, r := range res.Refused {
				fmt.Fprintf(out, "   %s: %s\n", r.Path, r.Reason)
			}
		}
	}
	st, err := remote.LoadState(brain)
	if err != nil {
		return err
	}
	if len(st.Conflicts) > 0 {
		what := fmt.Sprintf("%d files changed", len(st.Conflicts))
		if len(st.Conflicts) == 1 {
			what = "1 file changed"
		}
		fmt.Fprintf(out, "%s both here and on the server; compare with %s and run wong resolve:\n", what, filepath.Join(brain, ".wong", "conflicts"))
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
	client := remote.NewClient(cfg.URL, cfg.Token, timeout)
	if client.Device, err = remote.LoadDevice(); err != nil {
		return nil, err
	}
	return client, nil
}

// requireRemote is remoteClient for the commands that need a server.
func requireRemote(timeout time.Duration) (*remote.Client, error) {
	client, err := remoteClient(timeout)
	if err == nil && client == nil {
		err = errors.New("no server set: run wong setup <server-url>")
	}
	return client, err
}
