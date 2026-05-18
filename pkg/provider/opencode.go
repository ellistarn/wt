package provider

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FetchOpenCodeSessions queries the OpenCode SQLite database directly for all
// sessions across all projects. This avoids the `opencode session list` CLI
// which is project-scoped and only returns sessions for the current project.
func FetchOpenCodeSessions() SessionMap {
	dbPath := openCodeDBPath()
	if dbPath == "" {
		return nil
	}

	// Get the most recent non-subagent session per directory (across all projects).
	// Use ASCII Record Separator (0x1E) as column delimiter to avoid conflicts
	// with tabs or other characters in session titles.
	query := `SELECT id, title, time_updated, directory FROM session
		WHERE id IN (
			SELECT id FROM session s1
			WHERE s1.time_updated = (
				SELECT MAX(s2.time_updated) FROM session s2
				WHERE s2.directory = s1.directory AND (s2.parent_id IS NULL OR s2.parent_id = '')
			)
		) AND (parent_id IS NULL OR parent_id = '');`

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sqlite3", "-separator", "\x1e", dbPath, query).Output()
	if err != nil {
		return nil
	}

	result := make(SessionMap)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1e", 4)
		if len(parts) < 4 {
			continue
		}
		id := parts[0]
		title := parts[1]
		updatedMs, _ := strconv.ParseInt(parts[2], 10, 64)
		dir := parts[3]

		canonical := resolveDir(dir)
		if _, exists := result[canonical]; exists {
			continue
		}
		var activity time.Time
		if updatedMs > 0 {
			activity = time.UnixMilli(updatedMs)
		}
		result[canonical] = SessionInfo{
			ID:       id,
			Title:    title,
			Activity: activity,
		}
	}
	return result
}

// EnrichTokens adds token data from the OpenCode SQLite database to an
// existing SessionInfo. Uses the session ID directly when available to avoid
// path-matching issues. If the DB is unavailable or sqlite3 is not installed,
// the SessionInfo is unchanged.
func EnrichTokens(dir string, info *SessionInfo) {
	dbPath := openCodeDBPath()
	if dbPath == "" {
		return
	}

	var tokens, subTokens int64
	if info.ID != "" {
		tokens, subTokens = queryTokensByID(dbPath, info.ID)
	} else {
		canonical := resolveDir(dir)
		tokens, subTokens = queryTokensByDir(dbPath, canonical)
	}
	if tokens > 0 {
		info.Tokens = tokens
	}
	if subTokens > 0 {
		info.SubTokens = subTokens
	}
}

// queryTokensByID queries the DB for token counts using the session ID directly.
// Compatible with SQLite 3.7+ (no json_extract or CTEs required).
// Batches the parent and all child sessions into a single sqlite3 invocation.
func queryTokensByID(dbPath, sessionID string) (tokens, subTokens int64) {
	// Fetch session_id and data for the parent session AND all direct children
	// in one query. Use ASCII Record Separator as column delimiter and strip
	// newlines from data to ensure one record per line.
	query := `SELECT session_id, replace(data, char(10), '') FROM message
		WHERE (session_id = '` + escapeSQLite(sessionID) + `'
			OR session_id IN (SELECT id FROM session WHERE parent_id = '` + escapeSQLite(sessionID) + `'))
		AND data LIKE '%"tokens"%';`

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sqlite3", "-separator", "\x1e", dbPath, query).Output()
	if err != nil {
		return 0, 0
	}

	// Track max tokens per session_id
	maxBySession := make(map[string]int64)
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1e", 2)
		if len(parts) < 2 {
			continue
		}
		sid := parts[0]
		var msg struct {
			Tokens struct {
				Total int64 `json:"total"`
			} `json:"tokens"`
		}
		if err := json.Unmarshal([]byte(parts[1]), &msg); err != nil {
			continue
		}
		if msg.Tokens.Total > maxBySession[sid] {
			maxBySession[sid] = msg.Tokens.Total
		}
	}

	// Partition: parent session vs child sessions
	tokens = maxBySession[sessionID]
	for sid, max := range maxBySession {
		if sid != sessionID {
			subTokens += max
		}
	}
	return tokens, subTokens
}

// queryTokensByDir queries the DB for token counts using directory path matching.
func queryTokensByDir(dbPath, dir string) (tokens, subTokens int64) {
	// Find the most recent session ID for this directory
	idQuery := `SELECT id FROM session WHERE directory = '` + escapeSQLite(dir) + `'
		AND (parent_id IS NULL OR parent_id = '') ORDER BY time_updated DESC LIMIT 1;`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sqlite3", dbPath, idQuery).Output()
	if err != nil {
		return 0, 0
	}
	sessionID := strings.TrimSpace(string(out))
	if sessionID == "" {
		return 0, 0
	}
	return queryTokensByID(dbPath, sessionID)
}

// QueryOpenCodeDir checks for a .opencode/ directory in the worktree and uses
// the most recent file mtime as session activity. This serves as a last-resort
// fallback when both the CLI and database are unavailable.
func QueryOpenCodeDir(dir string) SessionInfo {
	ocDir := filepath.Join(dir, ".opencode")
	if _, err := os.Stat(ocDir); err != nil {
		return SessionInfo{}
	}

	var latest time.Time
	entries, err := os.ReadDir(ocDir)
	if err != nil {
		return SessionInfo{}
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}

	if latest.IsZero() {
		return SessionInfo{}
	}

	return SessionInfo{
		Activity: latest,
	}
}

func openCodeDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	// Respect $XDG_DATA_HOME (defaults to ~/.local/share per XDG spec)
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome != "" {
		path := filepath.Join(dataHome, "opencode", "opencode.db")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// Try ~/.local/share/opencode/opencode.db (Linux default)
	path := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if _, err := os.Stat(path); err == nil {
		return path
	}

	// Try macOS Application Support
	path = filepath.Join(home, "Library", "Application Support", "opencode", "opencode.db")
	if _, err := os.Stat(path); err == nil {
		return path
	}

	return ""
}

func escapeSQLite(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
