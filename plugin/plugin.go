// Package plugin holds the wong plugin for coding agents: its hooks and the maintain-brain skill.
package plugin

import _ "embed"

// Skill is the maintain-brain skill, for the agents that get it from wong setup rather than from a plugin.
//
//go:embed skills/maintain-brain/SKILL.md
var Skill []byte
