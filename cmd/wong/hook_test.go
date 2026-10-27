package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookNeverFailsTheSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	for _, input := range []string{"not json", `{"cwd": "` + home + `", "session_id": "s"}`} {
		for _, event := range []string{"session-start", "session-end"} {
			cmd := newRootCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetIn(strings.NewReader(input))
			cmd.SetArgs([]string{"--brain", filepath.Join(home, ".wong"), "hook", event})
			if err := cmd.Execute(); err != nil {
				t.Errorf("hook %s with %q: %v", event, input, err)
			}
		}
	}

	if _, err := run(t, "hook", "session-middle"); err == nil {
		t.Error("an unknown hook was accepted")
	}
}
