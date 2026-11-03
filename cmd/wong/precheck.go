package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/hook"
	"github.com/17tayyy/wong/internal/repos"
)

const precheckLine = "wong precheck"

func newPrecheckCmd(home string, brain *string) *cobra.Command {
	var install bool
	cmd := &cobra.Command{
		Use:   "precheck",
		Short: "Warn when a commit touches a file the brain notes a pitfall on",
		Long: "Warn when the staged changes touch a file that a pitfall of the brain cites: what\n" +
			"was tried and failed, or what is fragile there. It never stops the commit.\n\n" +
			"It is meant for the repo's git pre-commit hook; --install adds it there.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			top, err := gitOut("", "rev-parse", "--show-toplevel")
			if err != nil {
				return fmt.Errorf("not in a git repo: %w", err)
			}
			if install {
				return installPrecheck(cmd, top)
			}
			path, err := repos.DefaultPath()
			if err != nil {
				return err
			}
			reg, err := repos.Load(path)
			if err != nil {
				return err
			}
			staged, err := gitOut(top, "diff", "--cached", "--name-only", "-z")
			if err != nil {
				return err
			}
			env := hook.Env{Brain: *brain, Home: home, Repos: reg}
			var out strings.Builder
			for _, name := range strings.Split(strings.TrimRight(staged, "\x00"), "\x00") {
				if name == "" {
					continue
				}
				_, rel, hits := hook.Citing(filepath.Join(top, filepath.FromSlash(name)), env)
				var lines []string
				for _, h := range hits {
					if h.Pitfall {
						lines = append(lines, hook.Line(h, env))
					}
				}
				if len(lines) > 0 {
					fmt.Fprintf(&out, "%s:\n%s", rel, strings.Join(lines, ""))
				}
			}
			if out.Len() > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "wong: this commit touches files with a pitfall noted in the brain. Read it before you go on:\n%s", out.String())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&install, "install", false, "add wong precheck to this repo's pre-commit hook")
	return cmd
}

// installPrecheck writes a pre-commit hook that runs wong precheck, or says how to add it to the one the repo has.
func installPrecheck(cmd *cobra.Command, top string) error {
	dir, err := gitOut(top, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(top, dir)
	}
	path := filepath.Join(dir, "pre-commit")
	data, err := os.ReadFile(path)
	switch {
	case err == nil && strings.Contains(string(data), precheckLine):
		fmt.Fprintf(cmd.OutOrStdout(), "%s already runs wong precheck\n", path)
		return nil
	case err == nil:
		fmt.Fprintf(cmd.OutOrStdout(), "%s is already there; add this line to it, before any exit:\n  %s\n", path, precheckLine)
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+precheckLine+"\n"), 0o755); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Added wong precheck to %s\n", path)
	return nil
}

func gitOut(dir string, args ...string) (string, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exit.Stderr)))
		}
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}
