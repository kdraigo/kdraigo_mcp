// Package skills holds the Claude Code skills that ship with kdraigo-mcp.
// They are embedded so install-skill writes the version that matches the
// binary, offline, instead of whatever is on the main branch.
package skills

import "embed"

// FS holds one <name>.md per skill, with name and description front matter.
//
//go:embed *.md
var FS embed.FS
