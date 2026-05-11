package worktree

import (
	"crypto/rand"
	"encoding/hex"
	"path"
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

// StaleThreshold is the duration after which a session with no recent activity
// is considered stale.
const StaleThreshold = 4 * time.Hour

// Entry represents a discovered worktree.
type Entry struct {
	Name      string
	Dir       string
	Repo      string
	Host      string
	Branch    string
	CreatedAt time.Time
	UpdatedAt time.Time
	Status    string // working, idle, stale, or empty string (no session)
	Title     string
	Attached  bool
}

// HasSession reports whether this worktree has an active tmux session.
func (e Entry) HasSession() bool {
	return e.Status != "" || e.Attached
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

// TimeUnix converts a unix timestamp to time.Time.
func TimeUnix(sec int64) time.Time {
	return time.Unix(sec, 0)
}
