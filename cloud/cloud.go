// Package cloud embeds the default files harness setup writes into the VM.
package cloud

import _ "embed"

// DefaultClaudeMD is written to ~/.claude/CLAUDE.md by `harness setup`
// unless config.yml overrides it with claude_md.
//
//go:embed CLAUDE.md
var DefaultClaudeMD string
