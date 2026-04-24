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

// Sort sorts entries by most recent activity, newest first.
// Uses UpdatedAt if available, otherwise CreatedAt.
// Entries without any timestamp sort to the end.
func Sort(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		ti := sortTime(entries[i])
		tj := sortTime(entries[j])
		if ti.IsZero() && tj.IsZero() {
			return entries[i].Name < entries[j].Name
		}
		if ti.IsZero() {
			return false
		}
		if tj.IsZero() {
			return true
		}
		return ti.After(tj)
	})
}

func sortTime(e Entry) time.Time {
	if !e.UpdatedAt.IsZero() {
		return e.UpdatedAt
	}
	return e.CreatedAt
}

// TimeUnix converts a unix timestamp to time.Time.
func TimeUnix(sec int64) time.Time {
	return time.Unix(sec, 0)
}
