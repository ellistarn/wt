package opencode

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ellistarn/wt/pkg/ssh"
	"github.com/ellistarn/wt/pkg/worktree"
)

// sessionRecord holds the raw data from a SQLite query row.
type sessionRecord struct {
	id             string
	dir            string
	title          string
	updated        int64
	lastMsgRole    string
	lastMsgUpdated int64
}

func dbPath() string {
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataDir, "opencode", "opencode.db")
}

const sessionQuery = `
SELECT s.id, s.directory, s.title, s.time_updated,
       CASE WHEN m.data LIKE '%%"role":"assistant"%%' THEN 'assistant' ELSE '' END,
       COALESCE(m.time_updated, 0)
FROM session s
LEFT JOIN message m ON m.session_id = s.id
  AND m.time_created = (SELECT MAX(time_created) FROM message WHERE session_id = s.id)
WHERE s.directory IN (%s)
ORDER BY s.time_updated DESC;
`

func queryLocalSessions(dirs []string) ([]sessionRecord, error) {
	if len(dirs) == 0 {
		return nil, nil
	}
	return queryFromDB("", dbPath(), dirs)
}

func queryRemoteSessions(host string, dirs []string) ([]sessionRecord, error) {
	if len(dirs) == 0 {
		return nil, nil
	}
	return queryFromDB(host, "$HOME/.local/share/opencode/opencode.db", dirs)
}

func queryFromDB(host, dbFilePath string, dirs []string) ([]sessionRecord, error) {
	quoted := make([]string, len(dirs))
	for i, d := range dirs {
		quoted[i] = "'" + strings.ReplaceAll(d, "'", "''") + "'"
	}
	query := fmt.Sprintf(sessionQuery, strings.Join(quoted, ","))

	var out string
	var err error
	if host == "" {
		// If the DB doesn't exist locally, there are no sessions.
		if _, statErr := os.Stat(dbFilePath); os.IsNotExist(statErr) {
			return nil, nil
		}
		// Run sqlite3 locally, passing the query via stdin to avoid shell quoting issues.
		c := exec.Command("sqlite3", "-separator", "\t", dbFilePath)
		c.Stdin = strings.NewReader(query)
		b, e := c.Output()
		out, err = string(b), e
	} else {
		// Check if DB exists on remote before querying.
		if _, checkErr := ssh.Run(host, fmt.Sprintf("test -f %s", dbFilePath)); checkErr != nil {
			return nil, nil
		}
		// Use a heredoc to pass the query, avoiding shell quoting issues
		// with double quotes in the SQL LIKE clause.
		cmd := fmt.Sprintf("sqlite3 -separator $'\\t' \"%s\" <<'SQL'\n%s\nSQL", dbFilePath, query)
		out, err = ssh.Run(host, cmd)
	}
	if err != nil {
		return nil, fmt.Errorf("session query failed: %w", err)
	}

	var records []sessionRecord
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 6)
		if len(parts) < 6 {
			continue
		}
		var updated, lastMsgUpdated int64
		fmt.Sscanf(parts[3], "%d", &updated)
		fmt.Sscanf(parts[5], "%d", &lastMsgUpdated)
		records = append(records, sessionRecord{
			id:             parts[0],
			dir:            parts[1],
			title:          parts[2],
			updated:        updated,
			lastMsgRole:    parts[4],
			lastMsgUpdated: lastMsgUpdated,
		})
	}
	return records, nil
}

// EnrichLocal enriches worktree entries with local OpenCode session data.
func EnrichLocal(entries []worktree.Entry) error {
	dirs := make([]string, len(entries))
	for i, e := range entries {
		dirs[i] = e.Dir
	}
	records, err := queryLocalSessions(dirs)
	if err != nil {
		return err
	}
	enrich(entries, records)
	return nil
}

// EnrichRemote enriches worktree entries with remote OpenCode session data.
func EnrichRemote(host string, entries []worktree.Entry) error {
	dirs := make([]string, len(entries))
	for i, e := range entries {
		dirs[i] = e.Dir
	}
	records, err := queryRemoteSessions(host, dirs)
	if err != nil {
		return err
	}
	enrich(entries, records)
	return nil
}

func enrich(entries []worktree.Entry, records []sessionRecord) {
	byDir := make(map[string]sessionRecord)
	for _, r := range records {
		existing, ok := byDir[r.dir]
		if !ok || r.updated > existing.updated {
			byDir[r.dir] = r
		}
	}

	for i := range entries {
		r, ok := byDir[entries[i].Dir]
		if !ok {
			continue
		}
		entries[i].SessionID = r.id
		entries[i].Title = r.title
		entries[i].UpdatedAt = time.UnixMilli(r.updated)

		// Derive status from last message role and timing.
		// If the last message is from the assistant and was recently updated,
		// it may still be streaming (working). Otherwise idle.
		if r.lastMsgRole == "assistant" && r.lastMsgUpdated > 0 {
			msgTime := time.UnixMilli(r.lastMsgUpdated)
			// If the last assistant message was updated very recently (within 30s),
			// treat it as potentially still working.
			if time.Since(msgTime) < 30*time.Second {
				entries[i].Status = "working"
			} else {
				entries[i].Status = "idle"
			}
			entries[i].UpdatedAt = msgTime
		} else if r.id != "" {
			entries[i].Status = "idle"
		}
	}

	// Detect locally attached TUI clients.
	attached := AttachedDirs()
	for i := range entries {
		if count, ok := attached[entries[i].Dir]; ok && count > 0 {
			entries[i].Attached = true
		}
	}
}
