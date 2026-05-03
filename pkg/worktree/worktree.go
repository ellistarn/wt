package worktree

import (
	"crypto/rand"
	"encoding/hex"
	"path"
	"sort"
	"time"
)

// DefaultRoot is the default worktree root directory relative to the repo.
// Worktrees are placed at <repo>/<root>/<name>, so ".." puts them as siblings.
const DefaultRoot = ".."

// WorktreeDir returns the absolute path of a worktree: <repo>/<root>/<name>.
// The root is relative to the repo (default "..").
func WorktreeDir(repo, root, name string) string {
	return path.Join(repo, root, name)
}

// Entry represents a discovered worktree.
type Entry struct {
	Name   string // directory basename — the worktree's stable identity
	Dir    string // absolute path on the host where it lives
	Repo   string // repo root path
	Host   string // hostname where the worktree's server runs (empty = local)
	Branch string // checked-out branch (empty if HEAD is detached)
	CreatedAt time.Time // worktree creation time (from filesystem)
	UpdatedAt time.Time // last session activity (from OpenCode)
	SessionID string    // most recent OpenCode session ID (empty if none)
	Status    string    // working or idle; empty if no session
	Title     string    // OpenCode session title
	Tokens    int       // total input+output tokens in the most recent session
	Attached  bool      // true if a TUI client is attached to this worktree
}

// GenerateName returns a project-scoped name for a new worktree.
// Format: <project>-<hex> where project is the caller-supplied name
// and hex is 7 random hex chars (28 bits of entropy, ~268M namespace).
// The hyphen keeps the name flat — the same string is used as the git
// branch name, the worktree directory name, and the display label.
func GenerateName(project string) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return project + "-" + hex.EncodeToString(b[:])[:7]
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
