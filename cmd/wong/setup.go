package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/link"
	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/repos"
	"github.com/17tayyy/wong/plugin"
)

func newSetupCmd(home string, brain *string) *cobra.Command {
	var maps []string
	cmd := &cobra.Command{
		Use:   "setup [server-url]",
		Short: "Make the brain this machine's memory, connected to a wong server or on its own",
		Long: "Make the brain this machine's memory, in one go:\n\n" +
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
			"--map changes it. Run it again with a url to connect a machine set up on its own.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var client *remote.Client
			if len(args) == 1 {
				var err error
				if client, err = setRemote(cmd, args[0]); err != nil {
					return err
				}
				if client.Device, err = remote.LoadDevice(); err != nil {
					return err
				}
				if err := prepareKeys(cmd, client, *brain); err != nil {
					return err
				}
				if err := syncBrain(cmd, client, *brain, true, false); err != nil {
					return fmt.Errorf("the server did not answer as expected, check the url and the token: %w", err)
				}
			}
			from := filepath.Join(claudeConfigDir(home), "projects")
			if _, err := os.Stat(from); errors.Is(err, fs.ErrNotExist) {
				fmt.Fprintln(cmd.OutOrStdout(), "no Claude Code memory on this machine to import")
			} else if err := runImport(cmd, home, *brain, from, maps, false); err != nil {
				return err
			}
			if client != nil {
				if err := syncBrain(cmd, client, *brain, false, true); err != nil {
					return err
				}
			}
			if err := linkEverything(cmd, *brain, home); err != nil {
				return err
			}
			installPlugin(cmd)
			installCodex(cmd, home, *brain)
			installCursor(cmd, home)
			installOpenCode(cmd, home)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&maps, "map", nil, "send a directory or memory folder to a project: PATH=ID (ID \"me\" for global notes)")
	return cmd
}

// prepareKeys seals a new brain on a server that keeps them sealed, or waits for this machine's key to a sealed one.
func prepareKeys(cmd *cobra.Command, client *remote.Client, brain string) error {
	keys, err := client.Keys(cmd.Context())
	if err != nil {
		return fmt.Errorf("the server did not answer as expected, check the url and the token: %w", err)
	}
	switch {
	case keys.Sealed && keys.Wrapped == nil:
		return waitForKey(cmd, client)
	case !keys.Sealed && keys.Required:
		return sealBrain(cmd, client, brain, false)
	}
	return nil
}

// linkEverything links every repo wong knows to its project and makes me/ the memory of every other folder.
func linkEverything(cmd *cobra.Command, brain, home string) error {
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
	for _, t := range targets {
		if err := linkOne(t, reg, brain, home, false); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", t.dir, err)
			continue
		}
		fmt.Fprintf(out, "%s  -> %s\n", t.dir, link.MemoryDir(brain, home, "projects/"+t.project))
	}
	if err := reg.Save(path); err != nil {
		return err
	}
	return linkGlobal(out, brain, home, false)
}

var pluginSteps = [][]string{{"plugin", "marketplace", "add", "17tayyy/wong"}, {"plugin", "install", "wong@wong"}}

// installPlugin installs the wong plugin with Claude Code's own command, or says how to once Claude Code is there.
func installPlugin(cmd *cobra.Command) {
	out := cmd.OutOrStdout()
	manual := "  claude " + strings.Join(pluginSteps[0], " ") + "\n  claude " + strings.Join(pluginSteps[1], " ") + "\n"
	claude, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprint(out, "\nClaude Code is not on this machine's PATH. Once it is, install the wong plugin:\n"+manual)
		return
	}
	for i, step := range pluginSteps {
		result, err := exec.Command(claude, step...).CombinedOutput()
		if err != nil && (i > 0 || !strings.Contains(strings.ToLower(string(result)), "already")) {
			fmt.Fprintf(out, "\nInstalling the wong plugin failed (%s). Install it by hand:\n%s", strings.TrimSpace(string(result)), manual)
			return
		}
	}
	fmt.Fprintln(out, "\nInstalled the wong plugin for Claude Code. Restart open sessions to pick everything up.")
}

var codexSteps = [][]string{{"plugin", "marketplace", "add", "17tayyy/wong"}, {"plugin", "add", "wong@wong"}}

// installCodex installs the wong plugin in Codex, when Codex is on this machine, and lets Codex's sandbox write to the brain.
func installCodex(cmd *cobra.Command, home, brain string) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		return
	}
	out := cmd.OutOrStdout()
	for _, step := range codexSteps {
		result, err := exec.Command(codex, step...).CombinedOutput()
		if err != nil {
			fmt.Fprintf(out, "\nInstalling the wong plugin in Codex failed (%s). Install it by hand:\n  codex %s\n  codex %s\n",
				strings.TrimSpace(string(result)), strings.Join(codexSteps[0], " "), strings.Join(codexSteps[1], " "))
			return
		}
	}
	fmt.Fprintln(out, "\nInstalled the wong plugin for Codex. Codex runs no hook until you trust it: open Codex, run /hooks and trust wong's four hooks.")
	fmt.Fprintln(out, writableRoot(codexHome(home), brain))
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
func installCursor(cmd *cobra.Command, home string) {
	dir := filepath.Join(home, ".cursor")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return
	}
	out := cmd.OutOrStdout()
	hooks := filepath.Join(dir, "hooks.json")
	if err := setCursorHooks(hooks); err != nil {
		fmt.Fprintf(out, "\nAdding wong's hooks to Cursor failed (%v). Add them to %s by hand:\n", err, hooks)
		for _, h := range cursorHooks {
			fmt.Fprintf(out, "  %s: wong hook %s --agent cursor\n", h.event, h.hook)
		}
		return
	}
	skill := filepath.Join(dir, "skills", "maintain-brain", "SKILL.md")
	err := os.MkdirAll(filepath.Dir(skill), 0o755)
	if err == nil {
		err = os.WriteFile(skill, plugin.Skill, 0o644)
	}
	if err != nil {
		fmt.Fprintf(out, "\nAdded wong's hooks to Cursor, but writing its skill failed (%v).\n", err)
		return
	}
	fmt.Fprintf(out, "\nAdded wong's hooks to Cursor (%s) and its maintain-brain skill (%s). New chats pick them up.\n", hooks, filepath.Dir(skill))
}

// installOpenCode adds the wong plugin and the maintain-brain skill to OpenCode, when OpenCode is on this machine.
func installOpenCode(cmd *cobra.Command, home string) {
	dir := filepath.Join(cmp.Or(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config")), "opencode")
	if _, err := os.Stat(dir); err != nil {
		if _, err := exec.LookPath("opencode"); err != nil {
			return
		}
	}
	out := cmd.OutOrStdout()
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
			fmt.Fprintf(out, "\nAdding wong to OpenCode failed (%v).\n", err)
			return
		}
	}
	fmt.Fprintf(out, "\nAdded the wong plugin to OpenCode (%s) and its maintain-brain skill. New sessions pick them up.\n",
		filepath.Join(dir, "plugins", "wong.js"))
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
