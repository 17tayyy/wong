// Package plugin holds the wong plugin for coding agents: its hooks, the maintain-brain skill and the OpenCode plugin.
package plugin

import _ "embed"

// Skill is the maintain-brain skill, for the agents that get it from wong setup rather than from a plugin.
//
//go:embed skills/maintain-brain/SKILL.md
var Skill []byte

// OpenCode is the plugin wong setup installs in OpenCode.
//
//go:embed opencode/wong.js
var OpenCode []byte
