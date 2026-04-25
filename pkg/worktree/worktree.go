package worktree

import (
	"fmt"
	"math/rand"
	"sort"
	"time"
)

// Entry represents a discovered worktree.
type Entry struct {
	Name      string    // branch/worktree name
	Dir       string    // absolute path on the host where it lives
	Repo      string    // repo root path
	Remote    bool      // true if this worktree lives on the remote host
	CreatedAt time.Time // worktree creation time (from filesystem)
	UpdatedAt time.Time // last session activity (from OpenCode)
	SessionID string    // most recent OpenCode session ID (empty if none)
	Status    string    // working or idle; empty if no session
	Title     string    // OpenCode session title
	Tokens    int       // total input+output tokens in the most recent session
	Attached  bool      // true if a TUI client is attached to this worktree
}

// GenerateName returns a timestamped random name for a new worktree.
func GenerateName() string {
	now := time.Now()
	return fmt.Sprintf("%s-%d", now.Format("0102T1504"), rand.Intn(100000))
}

// Sort sorts entries by most recent activity (UpdatedAt), newest first.
// Entries without activity sort to the end, ordered by CreatedAt newest first.
func Sort(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		ai := entries[i].UpdatedAt
		aj := entries[j].UpdatedAt
		// Both have activity — sort by most recent
		if !ai.IsZero() && !aj.IsZero() {
			return ai.After(aj)
		}
		// Only one has activity — it wins
		if !ai.IsZero() {
			return true
		}
		if !aj.IsZero() {
			return false
		}
		// Neither has activity — sort by creation time
		if !entries[i].CreatedAt.IsZero() && !entries[j].CreatedAt.IsZero() {
			return entries[i].CreatedAt.After(entries[j].CreatedAt)
		}
		return entries[i].Name < entries[j].Name
	})
}

// TimeUnix converts a unix timestamp to time.Time.
func TimeUnix(sec int64) time.Time {
	return time.Unix(sec, 0)
}
