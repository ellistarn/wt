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

// FetchOpenCodeSessions calls `opencode session list` once and returns all
// sessions indexed by canonical directory. This is the primary data source
// for title and activity — it avoids DB path discovery and SQL path-matching
// issues entirely.
func FetchOpenCodeSessions() SessionMap {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "opencode", "session", "list", "--format", "json").Output()
	if err != nil {
		return nil
	}

	var sessions []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Updated   int64  `json:"updated"`
		Directory string `json:"directory"`
	}
	if err := json.Unmarshal(out, &sessions); err != nil {
		return nil
	}

	// Index by canonical directory. Sessions are returned most-recent-first,
	// so the first match per directory wins.
	result := make(SessionMap, len(sessions))
	for _, s := range sessions {
		canonical := resolveDir(s.Directory)
		if _, exists := result[canonical]; exists {
			continue // keep the most recent (first in list)
		}
		var activity time.Time
		if s.Updated > 0 {
			activity = time.UnixMilli(s.Updated)
		}
		result[canonical] = SessionInfo{
			ID:       s.ID,
			Title:    s.Title,
			Activity: activity,
		}
	}
	return result
}

// EnrichTokens adds token data from the OpenCode SQLite database to an
// existing SessionInfo. This is optional enrichment — if the DB is
// unavailable or sqlite3 is not installed, the SessionInfo is unchanged.
func EnrichTokens(dir string, info *SessionInfo) {
	dbPath := openCodeDBPath()
	if dbPath == "" {
		return
	}

	canonical := resolveDir(dir)
	tokens, subTokens := queryTokens(dbPath, canonical)
	if tokens > 0 {
		info.Tokens = tokens
	}
	if subTokens > 0 {
		info.SubTokens = subTokens
	}
}

// queryTokens queries the DB for token counts only.
func queryTokens(dbPath, dir string) (tokens, subTokens int64) {
	query := `
WITH RECURSIVE session_tree(id, depth) AS (
    SELECT id, 0 FROM session
    WHERE id = (SELECT id FROM session WHERE directory = '` + escapeSQLite(dir) + `' ORDER BY time_updated DESC LIMIT 1)
    UNION ALL
    SELECT s.id, st.depth + 1 FROM session s JOIN session_tree st ON s.parent_id = st.id
)
SELECT
    COALESCE((SELECT MAX(json_extract(m.data, '$.tokens.total'))
        FROM message m WHERE m.session_id = (SELECT id FROM session_tree WHERE depth = 0)
        AND json_extract(m.data, '$.tokens.total') IS NOT NULL), 0),
    COALESCE((SELECT SUM(session_max) FROM (
        SELECT MAX(json_extract(m.data, '$.tokens.total')) AS session_max
        FROM message m
        WHERE m.session_id IN (SELECT id FROM session_tree WHERE depth > 0)
        AND json_extract(m.data, '$.tokens.total') IS NOT NULL
        GROUP BY m.session_id
    )), 0);`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sqlite3", "-separator", "\t", dbPath, query).Output()
	if err != nil {
		return 0, 0
	}

	line := strings.TrimSpace(string(out))
	if line == "" {
		return 0, 0
	}

	parts := strings.SplitN(line, "\t", 2)
	if len(parts) < 2 {
		return 0, 0
	}

	if v, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
		tokens = v
	}
	if v, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
		subTokens = v
	}
	return tokens, subTokens
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
