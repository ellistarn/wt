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

// sessionRecord holds the fields needed from the SQLite session + last message.
type sessionRecord struct {
	id             string
	dir            string
	title          string
	updated        int64
	lastMsgRole    string
	lastMsgUpdated int64
}

// deriveStatus infers session status from the last message.
// While the agent is generating, the assistant message's time_updated advances
// as tokens stream in. Once complete, it stops updating.
func deriveStatus(r sessionRecord) string {
	if r.lastMsgRole == "assistant" && r.lastMsgUpdated > 0 {
		age := time.Since(time.UnixMilli(r.lastMsgUpdated))
		if age < 60*time.Second {
			return "working"
		}
	}
	return "idle"
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
ORDER BY s.time_updated DESC
`

func queryLocalSessions(dirs []string) []sessionRecord {
	if len(dirs) == 0 {
		return nil
	}
	return queryFromDB("", dbPath(), dirs)
}

func queryRemoteSessions(host string, dirs []string) []sessionRecord {
	if len(dirs) == 0 {
		return nil
	}
	return queryFromDB(host, "$HOME/.local/share/opencode/opencode.db", dirs)
}

func queryFromDB(host, dbFilePath string, dirs []string) []sessionRecord {
	quoted := make([]string, len(dirs))
	for i, d := range dirs {
		quoted[i] = "'" + strings.ReplaceAll(d, "'", "''") + "'"
	}
	query := fmt.Sprintf(sessionQuery, strings.Join(quoted, ","))

	var out string
	var err error
	if host == "" {
		// Run sqlite3 locally, passing the query via stdin to avoid shell quoting issues.
		c := exec.Command("sqlite3", "-separator", "\t", dbFilePath)
		c.Stdin = strings.NewReader(query)
		b, e := c.Output()
		out, err = string(b), e
	} else {
		// Escape double quotes so the LIKE pattern survives shell double-quoting.
		escaped := strings.ReplaceAll(query, `"`, `\"`)
		cmd := fmt.Sprintf("sqlite3 -separator '\t' \"%s\" \"%s\"", dbFilePath, escaped)
		out, err = ssh.Run(host, cmd)
	}
	if err != nil {
		return nil
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
	return records
}

// EnrichLocal enriches worktree entries with local OpenCode session data.
func EnrichLocal(entries []worktree.Entry) {
	dirs := make([]string, len(entries))
	for i, e := range entries {
		dirs[i] = e.Dir
	}
	enrich(entries, queryLocalSessions(dirs))
}

// EnrichRemote enriches worktree entries with remote OpenCode session data.
func EnrichRemote(host string, entries []worktree.Entry) {
	dirs := make([]string, len(entries))
	for i, e := range entries {
		dirs[i] = e.Dir
	}
	enrich(entries, queryRemoteSessions(host, dirs))
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
		entries[i].Status = deriveStatus(r)
	}
}
