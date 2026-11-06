package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/link"
	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/repos"
	"github.com/17tayyy/wong/internal/ui"
	"github.com/17tayyy/wong/plugin"
)

func newSetupCmd(home string, brain *string) *cobra.Command {
	var maps []string
	cmd := &cobra.Command{
		Use:   "setup [server-url]",
		Short: "Make the brain this machine's memory, connected to a wong server or on its own",
		Long: "Make the brain this machine's memory, in one go:\n\n" +
			"  0. without a url, in a terminal, asks whether to sync the brain with wong\n" +
			"     cloud, with a server of your own, or with none;\n" +
			"  1. with a server, connects this machine: it shows a code to enter in the\n" +
			"     dashboard, in a browser signed in to wong, or takes a token made on the\n" +
			"     server with wong serve --new-token <name>, pasted or read from stdin;\n" +
			"     seals a new brain on a server that keeps them end-to-end encrypted, or\n" +
			"     waits for another machine to give this one the key of a sealed brain;\n" +
			"     then pulls the brain, which is empty until the first machine pushes;\n" +
			"  2. imports this machine's Claude Code memory into it, as wong import does;\n" +
			"  3. with a server, pushes it, so the server holds the memory of every machine;\n" +
			"  4. links every repo it imported to its project, and every other folder to me/;\n" +
			"  5. installs the wong plugin for Claude Code, and for Codex when it is on\n" +
			"     this machine, letting Codex's sandbox write to the brain; with Cursor,\n" +
			"     adds wong's hooks to ~/.cursor/hooks.json and its skill to ~/.cursor/skills;\n" +
			"     with OpenCode, adds the wong plugin and the skill to ~/.config/opencode.\n\n" +
			"wong import --dry-run shows first how the memory will be grouped into projects;\n" +
			"--map changes it. Run it again to connect a machine set up on its own.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u := ui.New(cmd.OutOrStdout(), cmd.InOrStdin())
			defer u.Close()
			err := setup(cmd, u, home, *brain, maps, args)
			if err != nil && u.Fancy() {
				if errors.Is(err, ui.ErrCancelled) {
					u.Cancelled()
				} else {
					u.Fail(err.Error())
				}
				cmd.SilenceErrors = true
			}
			return err
		},
	}
	cmd.Flags().StringArrayVar(&maps, "map", nil, "send a directory or memory folder to a project: PATH=ID (ID \"me\" for global notes)")
	return cmd
}

const cloudURL = "https://wong-app.bytay.dev"

// setup connects this machine to the server given, or to the one picked in a terminal, then brings in its memory,
// links its folders to the brain and sets its agents up.
func setup(cmd *cobra.Command, u *ui.UI, home, brain string, maps, args []string) error {
	if os.Getenv("WONG_INSTALLER") != "" {
		u.Continue()
	} else {
		u.Intro(version())
	}
	url := ""
	if len(args) == 1 {
		url = args[0]
	} else if u.CanAsk() && !hasServer() {
		var err error
		if url, err = askServer(u); err != nil {
			return err
		}
	}
	var client *remote.Client
	if url != "" {
		var err error
		if client, err = setRemote(cmd, u, url); err != nil {
			return err
		}
		if client.Device, err = remote.LoadDevice(); err != nil {
			return err
		}
		if err := prepareKeys(cmd, u, client, brain); err != nil {
			return err
		}
		if err := setupSync(cmd, u, client, brain, true); err != nil {
			return fmt.Errorf("the server did not answer as expected, check the url and the token: %w", err)
		}
	}
	if err := setupImport(cmd, u, home, brain, maps); err != nil {
		return err
	}
	if client != nil {
		if err := setupSync(cmd, u, client, brain, false); err != nil {
			return err
		}
	}
	if err := linkEverything(cmd, u, brain, home); err != nil {
		return err
	}
	installPlugin(u)
	installCodex(u, home, brain)
	installCursor(u, home)
	installOpenCode(u, home)
	if filepath.Clean(brain) != filepath.Join(home, ".wong") && os.Getenv("WONG_BRAIN") != brain {
		u.Warn("This brain is not in ~/.wong: set WONG_BRAIN=" + brain + " in your shell's profile, so your agents' hooks and every wong command find it.")
	}
	switch {
	case client != nil:
		u.Outro("Done. New sessions of your agents start with the brain; restart the ones open now.")
	case hasServer():
		u.Outro("Done.")
	case u.Fancy():
		u.Outro("Done. The brain lives on this machine alone; run wong setup again to sync it with a server.")
	default:
		u.Outro("\nThe brain lives on this machine alone. To sync it with a wong server, yours or wong cloud\n" +
			"(" + cloudURL + "), run wong setup with the server's address.")
	}
	return nil
}

// askServer asks whether to sync the brain with wong cloud, with a server whose address it asks next, or with none.
func askServer(u *ui.UI) (string, error) {
	choice, err := u.Select("Sync this brain with a server?", []ui.Option{
		{Label: "No, keep it on this machine", Hint: "you can connect one later"},
		{Label: "Yes, with wong cloud", Hint: "by invitation, end-to-end encrypted"},
		{Label: "Yes, with my own wong server", Hint: "you type its address next"},
	}, 0)
	switch {
	case err != nil:
		return "", err
	case choice == 1:
		return cloudURL, nil
	case choice == 2:
		return u.Ask(ui.Text{Question: "Your server's address", Placeholder: "http://wong.lan:8787", Check: remote.CheckURL})
	}
	return "", nil
}

// setupSync pulls the brain from the server, or pushes it there, and says what changed.
func setupSync(cmd *cobra.Command, u *ui.UI, client *remote.Client, brain string, pull bool) error {
	if !u.Fancy() {
		return syncBrain(cmd, client, brain, pull, !pull)
	}
	if pull {
		sp := u.Spin("Pulling the brain from the server", "")
		res, err := remote.Pull(cmd.Context(), client, brain)
		if err != nil {
			sp.Stop("")
			return err
		}
		switch n := len(res.Updated); {
		case n == 0 && len(res.Deleted) == 0:
			sp.Stop("")
		case len(res.Deleted) == 0:
			sp.Stop("Pulled " + files(n) + " from the server")
		default:
			sp.Stop("Pulled " + files(n) + " from the server, and removed " + files(len(res.Deleted)) + " it no longer has")
		}
	} else {
		sp := u.Spin("Pushing the brain to the server", "")
		res, err := remote.Push(cmd.Context(), client, brain)
		if err != nil {
			sp.Stop("")
			return err
		}
		if n := len(res.Pushed); n == 0 {
			sp.Stop("Pushed to the server: nothing new")
		} else {
			sp.Stop("Pushed " + files(n) + " to the server")
		}
		if len(res.Refused) > 0 {
			var why []string
			for _, r := range res.Refused {
				why = append(why, r.Path+": "+r.Reason)
			}
			u.Warn(files(len(res.Refused))+" not pushed; fix them and run wong push:", why...)
		}
	}
	st, err := remote.LoadState(brain)
	if err != nil {
		return err
	}
	if len(st.Conflicts) > 0 {
		paths := slices.Sorted(maps.Keys(st.Conflicts))
		u.Warn(files(len(paths))+" changed both here and on the server; compare with "+filepath.Join(brain, ".wong", "conflicts")+" and run wong resolve:", paths...)
	}
	return nil
}

func files(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// setupImport brings this machine's Claude Code memory into the brain.
func setupImport(cmd *cobra.Command, u *ui.UI, home, brain string, maps []string) error {
	from := filepath.Join(claudeConfigDir(home), "projects")
	if _, err := os.Stat(from); errors.Is(err, fs.ErrNotExist) {
		u.Step("No Claude Code memory on this machine to import")
		return nil
	}
	if !u.Fancy() {
		return runImport(cmd, home, brain, from, maps, false)
	}
	sp := u.Spin("Importing Claude Code's memory", "")
	_, unmatched, res, err := importMemory(home, brain, from, maps, false)
	sp.Stop("")
	for _, folder := range unmatched {
		u.Warn("Skipped "+folder+": no folder on this machine matches it any more", "To bring its notes in, run wong import --map '"+folder+"=ID' with a project id, or me.")
	}
	for _, warning := range res.Warnings {
		u.Warn(warning)
	}
	if err != nil {
		return err
	}
	switch n := res.Written + res.Updated; {
	case n == 0 && res.Unchanged == 0:
		u.Step("No Claude Code memory on this machine to import")
	case n == 0:
		u.Step("Claude Code's memory was in the brain already")
	default:
		u.Step("Imported " + notes(n) + " from Claude Code")
	}
	for _, rel := range res.Conflicts {
		u.Warn(rel + " changed both in the brain and in Claude Code; it was left as it is")
	}
	return nil
}

func tilde(path, home string) string {
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return filepath.Join("~", rel)
}

// prepareKeys seals a new brain on a server that keeps them sealed, or waits for this machine's key to a sealed one.
func prepareKeys(cmd *cobra.Command, u *ui.UI, client *remote.Client, brain string) error {
	keys, err := client.Keys(cmd.Context())
	if err != nil {
		return fmt.Errorf("the server did not answer as expected, check the url and the token: %w", err)
	}
	switch {
	case keys.Sealed && keys.Wrapped == nil:
		if err := waitForKey(cmd, u, client); errors.Is(err, remote.ErrLocked) {
			return fmt.Errorf("%w; once it is approved, run wong setup %s again to finish setting this machine up", err, client.URL)
		} else if err != nil {
			return err
		}
		return nil
	case !keys.Sealed && keys.Required:
		return sealBrain(cmd, u, client, brain, false)
	}
	return nil
}

// linkEverything links every repo wong knows to its project and makes me/ the memory of every other folder.
func linkEverything(cmd *cobra.Command, u *ui.UI, brain, home string) error {
	path, err := repos.DefaultPath()
	if err != nil {
		return err
	}
	reg, err := repos.Load(path)
	if err != nil {
		return err
	}
	targets, err := linkTargets(reg, "", true)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if !u.Fancy() {
		fmt.Fprintln(out, "Claude Code keeps its memory in the brain now:")
	}
	var linked []linkTarget
	for _, t := range targets {
		if err := linkOne(t, reg, brain, home, false); err != nil {
			if u.Fancy() {
				u.Warn(tilde(t.dir, home) + ": " + err.Error())
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", t.dir, err)
			}
			continue
		}
		if !u.Fancy() {
			fmt.Fprintf(out, "%s  -> %s\n", t.dir, link.MemoryDir(brain, home, "projects/"+t.project))
		}
		linked = append(linked, t)
	}
	if err := reg.Save(path); err != nil {
		return err
	}
	if !u.Fancy() {
		return linkGlobal(out, brain, home, false)
	}
	if err := linkGlobal(io.Discard, brain, home, false); err != nil {
		return err
	}
	const most = 8
	rows := [][2]string{}
	for i, t := range linked {
		if i == most-1 && len(linked) > most {
			rows = append(rows, [2]string{fmt.Sprintf("and %d more repos", len(linked)-i), ""})
			break
		}
		rows = append(rows, [2]string{tilde(t.dir, home), t.project})
	}
	rows = append(rows, [2]string{"every other folder", "me"})
	width := 0
	for _, r := range rows {
		width = max(width, len([]rune(r[0])))
	}
	var lines []string
	for _, r := range rows {
		line := "  " + r[0]
		if r[1] != "" {
			line += strings.Repeat(" ", width-len([]rune(r[0]))) + "  " + u.Dim("→") + " " + u.Accent(r[1])
		}
		lines = append(lines, line)
	}
	u.Step("Claude Code keeps its memory in the brain", lines...)
	return nil
}

var pluginSteps = [][]string{{"plugin", "marketplace", "add", "17tayyy/wong"}, {"plugin", "install", "wong@wong"}}

// installPlugin installs the wong plugin with Claude Code's own command, or says how to once Claude Code is there.
func installPlugin(u *ui.UI) {
	manual := []string{"  claude " + strings.Join(pluginSteps[0], " "), "  claude " + strings.Join(pluginSteps[1], " ")}
	claude, err := exec.LookPath("claude")
	if err != nil {
		u.Warn("Claude Code is not on this machine's PATH. Once it is, install the wong plugin:", manual...)
		return
	}
	sp := u.Spin("Installing the wong plugin for Claude Code", "")
	for i, step := range pluginSteps {
		result, err := exec.Command(claude, step...).CombinedOutput()
		if err != nil && (i > 0 || !strings.Contains(strings.ToLower(string(result)), "already")) {
			sp.Fail("Installing the wong plugin for Claude Code failed ("+strings.TrimSpace(string(result))+"). Install it by hand:", manual...)
			return
		}
	}
	sp.Stop("Installed the wong plugin for Claude Code")
}

var codexSteps = [][]string{{"plugin", "marketplace", "add", "17tayyy/wong"}, {"plugin", "add", "wong@wong"}}

// installCodex installs the wong plugin in Codex, when Codex is on this machine, and lets Codex's sandbox write to the brain.
func installCodex(u *ui.UI, home, brain string) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		return
	}
	sp := u.Spin("Installing the wong plugin for Codex", "")
	for _, step := range codexSteps {
		result, err := exec.Command(codex, step...).CombinedOutput()
		if err != nil {
			sp.Fail("Installing the wong plugin for Codex failed ("+strings.TrimSpace(string(result))+"). Install it by hand:",
				"  codex "+strings.Join(codexSteps[0], " "), "  codex "+strings.Join(codexSteps[1], " "))
			return
		}
	}
	details := []string{"Codex runs no hook until you trust it: open Codex, run /hooks and trust wong's four hooks."}
	if root := writableRoot(codexHome(home), brain); root != "" {
		details = append(details, root)
	}
	sp.Stop("Installed the wong plugin for Codex", details...)
}

func codexHome(home string) string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(home, ".codex")
}

// writableRoot adds the brain to the folders Codex's workspace-write sandbox may write, or says how to when the config already sets them.
func writableRoot(codexHome, brain string) string {
	config := filepath.Join(codexHome, "config.toml")
	data, err := os.ReadFile(config)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("Reading %s failed (%v); add %s to sandbox_workspace_write.writable_roots there.", config, err, brain)
	}
	if strings.Contains(string(data), brain) {
		return ""
	}
	if strings.Contains(string(data), "sandbox_workspace_write") || strings.Contains(brain, "'") {
		return fmt.Sprintf("For Codex to write notes without asking each time, add %s to sandbox_workspace_write.writable_roots in %s.", brain, config)
	}
	f, err := os.OpenFile(config, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, err = fmt.Fprintf(f, "%s[sandbox_workspace_write]\nwritable_roots = ['%s']\n", separator(data), brain)
		err = errors.Join(err, f.Close())
	}
	if err != nil {
		return fmt.Sprintf("Writing %s failed (%v); add %s to sandbox_workspace_write.writable_roots there.", config, err, brain)
	}
	return fmt.Sprintf("Codex may now write notes in %s as well as in the repo it works on (sandbox_workspace_write.writable_roots in %s).", brain, config)
}

var cursorHooks = []struct {
	event, matcher, hook string
	timeout              int
}{
	{"sessionStart", "", "session-start", 30},
	{"sessionEnd", "", "session-end", 10},
	{"preToolUse", "^(Read|Write|Delete|Shell)$", "pre-tool-use", 5},
	{"postToolUse", "^Write$", "post-tool-use", 5},
}

// installCursor adds wong's hooks and the maintain-brain skill to Cursor, when Cursor is on this machine.
func installCursor(u *ui.UI, home string) {
	dir := filepath.Join(home, ".cursor")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return
	}
	hooks := filepath.Join(dir, "hooks.json")
	if err := setCursorHooks(hooks); err != nil {
		var manual []string
		for _, h := range cursorHooks {
			manual = append(manual, fmt.Sprintf("  %s: wong hook %s --agent cursor", h.event, h.hook))
		}
		u.Warn(fmt.Sprintf("Adding wong's hooks to Cursor failed (%v). Add them to %s by hand:", err, hooks), manual...)
		return
	}
	skill := filepath.Join(dir, "skills", "maintain-brain", "SKILL.md")
	err := os.MkdirAll(filepath.Dir(skill), 0o755)
	if err == nil {
		err = os.WriteFile(skill, plugin.Skill, 0o644)
	}
	if err != nil {
		u.Warn(fmt.Sprintf("Added wong's hooks to Cursor, but writing its skill failed (%v)", err))
		return
	}
	u.Step("Added wong's hooks to Cursor, and its maintain-brain skill", "New chats pick them up.")
}

// installOpenCode adds the wong plugin and the maintain-brain skill to OpenCode, when OpenCode is on this machine.
func installOpenCode(u *ui.UI, home string) {
	dir := filepath.Join(cmp.Or(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config")), "opencode")
	if _, err := os.Stat(dir); err != nil {
		if _, err := exec.LookPath("opencode"); err != nil {
			return
		}
	}
	files := map[string][]byte{
		filepath.Join(dir, "plugins", "wong.js"):                   plugin.OpenCode,
		filepath.Join(dir, "skills", "maintain-brain", "SKILL.md"): plugin.Skill,
	}
	for path, data := range files {
		err := os.MkdirAll(filepath.Dir(path), 0o755)
		if err == nil {
			err = os.WriteFile(path, data, 0o644)
		}
		if err != nil {
			u.Warn(fmt.Sprintf("Adding wong to OpenCode failed (%v)", err))
			return
		}
	}
	u.Step("Added the wong plugin to OpenCode, and its maintain-brain skill", "New sessions pick them up.")
}

// setCursorHooks adds wong's hooks to a Cursor hooks.json, replacing the ones a previous wong added and keeping every other.
func setCursorHooks(path string) error {
	config := map[string]any{}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("%s does not parse: %w", path, err)
		}
	}
	if config["hooks"] == nil {
		config["hooks"] = map[string]any{}
	}
	hooks, ok := config["hooks"].(map[string]any)
	if !ok {
		return fmt.Errorf("hooks in %s is not an object", path)
	}
	for _, h := range cursorHooks {
		if hooks[h.event] == nil {
			hooks[h.event] = []any{}
		}
		list, ok := hooks[h.event].([]any)
		if !ok {
			return fmt.Errorf("hooks.%s in %s is not a list", h.event, path)
		}
		list = slices.DeleteFunc(list, func(e any) bool {
			entry, _ := e.(map[string]any)
			command, _ := entry["command"].(string)
			return strings.HasPrefix(command, "wong hook ")
		})
		entry := map[string]any{"command": "wong hook " + h.hook + " --agent cursor", "timeout": h.timeout}
		if h.matcher != "" {
			entry["matcher"] = h.matcher
		}
		hooks[h.event] = append(list, entry)
	}
	if config["version"] == nil {
		config["version"] = 1
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(config); err != nil {
		return err
	}
	if bytes.Equal(buf.Bytes(), data) {
		return nil
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func separator(data []byte) string {
	switch {
	case len(data) == 0:
		return ""
	case data[len(data)-1] != '\n':
		return "\n\n"
	}
	return "\n"
}

// hasServer reports whether this machine is connected to a wong server already.
func hasServer() bool {
	path, err := remote.ConfigPath()
	if err != nil {
		return false
	}
	_, ok, _ := remote.LoadConfig(path)
	return ok
}
