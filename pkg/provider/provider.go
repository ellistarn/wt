package provider

import (
	"path/filepath"
	"strings"
	"time"
)

// SessionInfo contains information about an agent session in a directory.
type SessionInfo struct {
	Title     string
	Activity  time.Time // last update time of the session
	Tokens    int64     // tokens consumed in the base session
	SubTokens int64     // tokens consumed across subagent sessions
}

// Query returns session info for a worktree directory.
// It tries providers based on the command name.
// Returns zero SessionInfo if no provider matches or has data.
func Query(dir, cmd string) SessionInfo {
	switch baseCmd(cmd) {
	case "opencode":
		return queryOpenCode(dir)
	case "claude":
		return queryClaude(dir)
	default:
		// Try all providers, return first match
		if info := queryOpenCode(dir); info != (SessionInfo{}) {
			return info
		}
		return queryClaude(dir)
	}
}

func baseCmd(cmd string) string {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ""
	}
	return filepath.Base(parts[0])
}
