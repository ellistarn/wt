package provider

import (
	"path/filepath"
	"strings"
	"time"
)

// SessionInfo contains information about an agent session in a directory.
type SessionInfo struct {
	ID        string    // session identifier (for resume)
	Title     string
	Activity  time.Time // last update time of the session
	Tokens    int64     // tokens consumed in the base session
	SubTokens int64     // tokens consumed across subagent sessions
}

// SessionMap holds sessions indexed by canonical (symlink-resolved) directory.
type SessionMap map[string]SessionInfo

// Match finds the session for a directory, resolving symlinks for comparison.
func (m SessionMap) Match(dir string) SessionInfo {
	if m == nil {
		return SessionInfo{}
	}
	canonical := resolveDir(dir)
	return m[canonical]
}

// MatchID returns the session ID for a directory, or "" if not found.
func (m SessionMap) MatchID(dir string) string {
	return m.Match(dir).ID
}

// BaseCmd extracts the base command name from a full command string.
func BaseCmd(cmd string) string {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ""
	}
	return filepath.Base(parts[0])
}

// resolveDir returns the canonical path for a directory (symlinks resolved).
func resolveDir(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return filepath.Clean(dir)
}
