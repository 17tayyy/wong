package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/remote"
)

var manifestPage = template.Must(template.New("manifest").Parse(`<!doctype html>
<meta charset="utf-8"><title>Make wong's GitHub App</title>
<form method="post" action="{{.Action}}">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<p>Taking you to GitHub to make the app…</p>
<button>Go on to GitHub</button>
</form>
<script>document.forms[0].submit()</script>`))

const madePage = `<!doctype html>
<meta charset="utf-8"><title>wong's GitHub App</title>
<p>%s You can close this tab.</p>`

func newGitHubAppCmd() *cobra.Command {
	var server, name, org, out string
	var public bool
	cmd := &cobra.Command{
		Use:   "github-app",
		Short: "Make the GitHub App a wong server takes pushes and pull requests from",
		Long: "Make the GitHub App a wong server takes pushes and pull requests from, so that people\n" +
			"connect GitHub from the dashboard's Webhooks page instead of adding a webhook to each repo.\n\n" +
			"It opens GitHub in your browser with the app filled in: its webhook at the server's\n" +
			"/hooks/github-app, read access to pull requests and to code, which GitHub asks of any app\n" +
			"that takes pushes, and the right to post checks. Once you make it, GitHub hands its keys\n" +
			"back to this command, which writes them to --out, readable by you alone. Start the\n" +
			"server with --github-app and that file.\n\n" +
			"--public lets anyone install the app, as on a server with accounts for many people; by\n" +
			"default only the account that makes it can. --org makes it for an organization.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			base, err := url.Parse(strings.TrimSuffix(server, "/"))
			if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
				return errors.New("--url must be the server's address, such as https://wong.example.com")
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return err
			}
			defer ln.Close()
			local := "http://" + ln.Addr().String()
			raw := make([]byte, 16)
			rand.Read(raw)
			state := hex.EncodeToString(raw)
			manifest, err := json.Marshal(map[string]any{
				"name":            cmp.Or(name, "wong "+base.Hostname()),
				"url":             base.String(),
				"description":     "Leaves tasks on the brain's notes a push makes outdated, and checks each pull request against them.",
				"hook_attributes": map[string]any{"url": base.String() + "/hooks/github-app"},
				"redirect_url":    local + "/made",
				"callback_urls":   []string{base.String() + "/webhooks/github/callback"},
				"setup_url":       base.String() + "/webhooks/github/setup",
				"setup_on_update": true,
				"public":          public,
				"default_permissions": map[string]string{
					"contents": "read", "pull_requests": "read", "checks": "write", "metadata": "read",
				},
				"default_events": []string{"push", "pull_request"},
			})
			if err != nil {
				return err
			}
			action := "https://github.com/settings/apps/new"
			if org != "" {
				action = "https://github.com/organizations/" + url.PathEscape(org) + "/settings/apps/new"
			}
			action += "?state=" + state
			done := make(chan error, 1)
			var made *remote.GitHubApp
			mux := http.NewServeMux()
			mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
				manifestPage.Execute(w, map[string]string{"Action": action, "Manifest": string(manifest)})
			})
			mux.HandleFunc("GET /made", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("state") != state {
					http.Error(w, "this is not the app this command asked for", http.StatusBadRequest)
					return
				}
				data, err := remote.ConvertManifest(r.Context(), "", r.URL.Query().Get("code"))
				if err == nil {
					made, err = remote.SaveGitHubApp(out, data)
				}
				if err != nil {
					fmt.Fprintf(w, madePage, "wong could not keep the app's keys: "+template.HTMLEscapeString(err.Error())+".")
				} else {
					fmt.Fprintf(w, madePage, "The app is made, and its keys are saved.")
				}
				select {
				case done <- err:
				default:
				}
			})
			srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
			go srv.Serve(ln)
			defer srv.Close()
			fmt.Fprintf(cmd.OutOrStdout(), "Opening %s to make the app on GitHub. If no browser opens, open it yourself.\n", local)
			if err := openBrowser(local); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "could not open a browser here: open the address above")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), time.Hour)
			defer cancel()
			select {
			case err := <-done:
				if err != nil {
					return err
				}
			case <-ctx.Done():
				return errors.New("no app was made within the hour GitHub gives")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Made %s (https://github.com/apps/%s) and saved its keys in %s, readable by you alone.\n"+
				"Start the server with --github-app pointing at that file, and connect GitHub from the dashboard's Webhooks page.\n", made.Name, made.Slug, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "url", "", "the server's address, as GitHub reaches it (required)")
	cmd.Flags().StringVar(&name, "name", "", "the app's name, which must be free on GitHub (default \"wong <host>\")")
	cmd.Flags().StringVar(&org, "org", "", "make the app for this organization instead of your account")
	cmd.Flags().BoolVar(&public, "public", false, "let any GitHub account install the app")
	cmd.Flags().StringVar(&out, "out", "github-app.json", "where to write the app's keys")
	_ = cmd.MarkFlagRequired("url")
	return cmd
}
