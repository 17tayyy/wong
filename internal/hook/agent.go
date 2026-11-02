package hook

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Agent is the coding agent whose hooks wong answers.
type Agent string

const (
	Claude Agent = "claude"
	Codex  Agent = "codex"
)

const (
	maxCommandFiles = 5
	maxCommandScan  = 4096
)

var patchMarkers = []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "}

// touched lists the files a tool call works on, absolute, and the ones it creates; with reads, also the files a shell command names.
func touched(in Input, env Env, reads bool) ([]string, map[string]bool) {
	created := map[string]bool{}
	switch {
	case in.ToolInput.FilePath != "":
		if in.ToolName == "Write" {
			created[in.ToolInput.FilePath] = true
		}
		return []string{in.ToolInput.FilePath}, created
	case in.ToolName == "apply_patch":
		return patchFiles(in.ToolInput.Command, in.Cwd, created), created
	case in.ToolName == "Bash" && reads:
		return commandFiles(in.ToolInput.Command, in.Cwd, env.Home), created
	}
	return nil, created
}

// patchFiles lists the files an apply_patch patch adds, changes, deletes or moves to, marking the ones it adds.
func patchFiles(patch, cwd string, created map[string]bool) []string {
	var files []string
	for line := range strings.Lines(patch) {
		for i, marker := range patchMarkers {
			name, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), marker)
			if !ok || strings.TrimSpace(name) == "" {
				continue
			}
			file := absolute(strings.TrimSpace(name), cwd, "")
			if file == "" {
				continue
			}
			if i == 0 {
				created[file] = true
			}
			files = append(files, file)
		}
	}
	return files
}

// commandFiles lists the existing files a shell command names, such as the one cat or sed reads.
func commandFiles(command, cwd, home string) []string {
	if len(command) > maxCommandScan {
		command = command[:maxCommandScan]
	}
	var files []string
	seen := map[string]bool{}
	for _, word := range words(command) {
		if strings.HasPrefix(word, "-") || strings.ContainsAny(word, "$*?`=") {
			continue
		}
		file := absolute(word, cwd, home)
		if file == "" || seen[file] {
			continue
		}
		if info, err := os.Stat(file); err != nil || !info.Mode().IsRegular() {
			continue
		}
		seen[file] = true
		files = append(files, file)
		if len(files) == maxCommandFiles {
			break
		}
	}
	return files
}

// words splits a shell command into its words, keeping quoted text together and breaking at operators.
func words(command string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	inWord := false
	flush := func() {
		if inWord {
			out = append(out, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case unicode.IsSpace(r) || strings.ContainsRune("|&;<>()", r):
			flush()
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	flush()
	return out
}

func absolute(name, cwd, home string) string {
	if rest, ok := strings.CutPrefix(name, "~/"); ok && home != "" {
		return filepath.Join(home, rest)
	}
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	if cwd == "" {
		return ""
	}
	return filepath.Join(cwd, name)
}
