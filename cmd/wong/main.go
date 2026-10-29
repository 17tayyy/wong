package main

import (
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	home, _ := os.UserHomeDir()
	root := &cobra.Command{
		Use:          "wong",
		Short:        "An external brain for coding agents.",
		Version:      version(),
		SilenceUsage: true,
	}
	brain := root.PersistentFlags().String("brain", filepath.Join(home, ".wong"), "brain directory")
	root.AddCommand(newSetupCmd(home, brain), newImportCmd(home, brain), newCheckCmd(brain), newLinkCmd(home, brain), newNoteCmd(home, brain),
		newTagsCmd(brain), newObsidianCmd(brain), newTidyCmd(brain), newSkillsCmd(home, brain), newServeCmd(), newRemoteCmd(), newTasksCmd(), newMCPCmd(), newHookCmd(home, brain))
	root.AddCommand(newSyncCmds(brain)...)
	return root
}

// release is the version a release build sets with -ldflags "-X main.release=v1.2.3".
var release string

func version() string {
	if release != "" {
		return release
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "dev"
}
