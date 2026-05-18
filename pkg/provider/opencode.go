package provider

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func queryOpenCode(dir string) SessionInfo {
	// Try SQLite database first (primary source of truth)
	if info := queryOpenCodeDB(dir); info != (SessionInfo{}) {
		return info
	}

	// Fallback: check for .opencode/ directory in the worktree
	return queryOpenCodeDir(dir)
}

func queryOpenCodeDB(dir string) SessionInfo {
	dbPath := openCodeDBPath()
	if dbPath == "" {
		return SessionInfo{}
	}

	// Single query: get title, activity, and tokens split into base session vs
	// subagents. The recursive CTE traverses parent_id to find subagent sessions.
	// Tokens use MAX per session (the value is cumulative within a session).
	query := `
WITH RECURSIVE session_tree(id, depth) AS (
    SELECT id, 0 FROM session
    WHERE id = (SELECT id FROM session WHERE directory = '` + escapeSQLite(dir) + `' ORDER BY time_updated DESC LIMIT 1)
    UNION ALL
    SELECT s.id, st.depth + 1 FROM session s JOIN session_tree st ON s.parent_id = st.id
)
SELECT
    (SELECT title FROM session WHERE id = (SELECT id FROM session_tree LIMIT 1)),
    (SELECT time_updated FROM session WHERE id = (SELECT id FROM session_tree LIMIT 1)),
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
		return SessionInfo{}
	}

	line := strings.TrimSpace(string(out))
	if line == "" {
		return SessionInfo{}
	}

	parts := strings.SplitN(line, "\t", 4)
	if len(parts) < 4 {
		return SessionInfo{}
	}

	title := parts[0]

	var activity time.Time
	if ts, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
		activity = time.UnixMilli(ts)
	}

	var tokens int64
	if v, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
		tokens = v
	}

	var subTokens int64
	if v, err := strconv.ParseInt(parts[3], 10, 64); err == nil {
		subTokens = v
	}

	if title == "" && activity.IsZero() && tokens == 0 && subTokens == 0 {
		return SessionInfo{}
	}

	return SessionInfo{
		Title:     title,
		Activity:  activity,
		Tokens:    tokens,
		SubTokens: subTokens,
	}
}

// queryOpenCodeDir checks for a .opencode/ directory in the worktree and uses
// the most recent file mtime as session activity. This serves as a fallback
// when the SQLite database is unavailable.
func queryOpenCodeDir(dir string) SessionInfo {
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

	// Try ~/.local/share/opencode/opencode.db first (Linux / observed macOS location)
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
