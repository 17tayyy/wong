package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Agent is the coding agent whose hooks wong answers.
type Agent string

const (
	Claude Agent = "claude"
	Codex  Agent = "codex"
	Cursor Agent = "cursor"
)

const (
	maxCommandFiles = 5
	maxCommandScan  = 4096
	// maxContext is the most Cursor takes from a hook: it drops any longer context whole.
	maxContext = 10000
)

var patchMarkers = []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "}

// touched lists the files a tool call works on, absolute, and the ones it creates; with reads, also the files a shell command names.
func touched(in Input, env Env, reads bool) ([]string, map[string]bool) {
	created := map[string]bool{}
	switch {
	case in.ToolInput.FilePath != "":
		if in.ToolName == "Write" && !reads {
			created[in.ToolInput.FilePath] = env.Agent != Cursor || !tracked(env.Brain, in.ToolInput.FilePath)
		}
		return []string{in.ToolInput.FilePath}, created
	case in.ToolName == "apply_patch":
		return patchFiles(in.ToolInput.Command, in.Cwd, created), created
	case (in.ToolName == "Bash" || in.ToolName == "Shell") && reads:
		cwd := in.Cwd
		if in.ToolInput.WorkingDirectory != "" {
			cwd = absolute(in.ToolInput.WorkingDirectory, in.Cwd, env.Home)
		}
		return commandFiles(in.ToolInput.Command, cwd, env.Home), created
	}
	return nil, created
}

// tracked says whether git tracks a file of the brain, which tells a note Cursor wrote anew from one it edited.
func tracked(brain, file string) bool {
	rel, err := filepath.Rel(filepath.Clean(brain), filepath.Clean(file))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	_, err = git(brain, "ls-files", "--error-unmatch", "--", filepath.ToSlash(rel))
	return err == nil
}

// answer puts what a hook tells the agent in that agent's format for the event: SessionStart, PreToolUse or PostToolUse.
func answer(agent Agent, event, text string) string {
	switch {
	case agent == Cursor:
		if text == "" {
			return "{}"
		}
		out, _ := json.Marshal(map[string]string{"additional_context": clip(text, maxContext)})
		return string(out)
	case text == "":
		return ""
	case event == "SessionStart":
		return text
	case event == "PostToolUse" && agent != Codex:
		out, _ := json.Marshal(map[string]string{"decision": "block", "reason": text})
		return string(out)
	}
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": event, "additionalContext": text}})
	return string(out)
}

// clip cuts text at a line to fit limit bytes, saying so.
func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	const tail = "\n(wong cut this short to fit what the agent takes from a hook)"
	cut := text[:limit-len(tail)]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + tail
}

// hasMemory says whether the agent keeps a memory of its own that wong links to the brain, as Claude Code does.
func (a Agent) hasMemory() bool {
	return a != Codex && a != Cursor
}

// skill names the maintain-brain skill as the agent knows it.
func skill(agent Agent) string {
	if agent == Cursor {
		return "maintain-brain"
	}
	return "wong:maintain-brain"
}

// ReadInput reads a hook's JSON input, taking the workspace for the working directory when the agent sends none.
func ReadInput(raw []byte) (Input, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, fmt.Errorf("reading the hook input: %w", err)
	}
	if in.Cwd == "" && len(in.WorkspaceRoots) > 0 {
		in.Cwd = in.WorkspaceRoots[0]
	}
	return in, nil
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
