package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/17tayyy/wong/internal/brain"
	"github.com/17tayyy/wong/internal/repos"
	"github.com/17tayyy/wong/internal/tidy"
)

func newTidyCmd(brainDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "tidy",
		Short: "List preferences that repeat each other and notes that seem to be in the wrong folder",
		Long: "List preferences that repeat each other and notes that seem to be in the wrong folder.\n\n" +
			"It only reports: merging and moving notes across projects is for the user to\n" +
			"decide, then wong note move and wong note remove do it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			notes, err := tidy.Load(*brainDir)
			if err != nil {
				return err
			}
			names, err := projectNames(*brainDir)
			if err != nil {
				return err
			}
			describe := map[string]string{}
			for _, n := range notes {
				describe[n.Path] = n.Description
			}
			out := cmd.OutOrStdout()
			dupes := tidy.Duplicates(notes)
			fmt.Fprintf(out, "Preferences that may say the same thing (%d groups):\n", len(dupes))
			for i, group := range dupes {
				for j, path := range group {
					prefix := "    "
					if j == 0 {
						prefix = fmt.Sprintf("%2d) ", i+1)
					}
					fmt.Fprintf(out, "%s%s — %s\n", prefix, path, shorten(describe[path], 90))
				}
			}
			moves := tidy.Misplaced(notes, names)
			fmt.Fprintf(out, "\nNotes that may be in the wrong folder (%d):\n", len(moves))
			for _, m := range moves {
				fmt.Fprintf(out, "    %s -> %s (names it %d times, its own folder %d)\n", m.Note, m.To, m.Score, m.Others)
			}
			return nil
		},
	}
}

// projectNames maps each project of the brain to its id and the directory names of its repos.
func projectNames(brainDir string) (map[string][]string, error) {
	folders, err := brain.Folders(brainDir)
	if err != nil {
		return nil, err
	}
	path, err := repos.DefaultPath()
	if err != nil {
		return nil, err
	}
	reg, err := repos.Load(path)
	if err != nil {
		return nil, err
	}
	names := map[string][]string{}
	for _, folder := range folders {
		id, ok := strings.CutPrefix(folder, "projects/")
		if !ok {
			continue
		}
		names[id] = []string{id}
		if dotted := strings.ReplaceAll(id, "-", "."); dotted != id {
			names[id] = append(names[id], dotted)
		}
		for _, dir := range reg[id] {
			if base := strings.ToLower(filepath.Base(dir)); !slices.Contains(names[id], base) {
				names[id] = append(names[id], base)
			}
		}
	}
	return names, nil
}

func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
