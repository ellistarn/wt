package worktree

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
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
	Branch    string
	CreatedAt time.Time
	UpdatedAt time.Time // most recent activity timestamp from provider
	Status    string    // "active" or "" (no active session)
	Title     string    // from agent state store via provider
	Tokens    int64     // total tokens consumed in session (including subagents)
}

// HasSession reports whether this worktree has an active or historical session.
// True if there's a running opencode process (Status set) or .opencode/ dir exists (UpdatedAt set).
func (e Entry) HasSession() bool {
	return e.Status != "" || !e.UpdatedAt.IsZero()
}

// GenerateName returns a project-scoped name for a new worktree.
// Format: <project>-<hex> where project is the caller-supplied name
// and hex is 7 random hex chars (28 bits of entropy, ~268M namespace).
func GenerateName(project string) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return project + "-" + hex.EncodeToString(b[:])[:7]
}

// Metadata holds per-worktree configuration stored in git's worktree metadata directory.
type Metadata struct {
	Cmd   string `json:"cmd,omitempty"`
	Title string `json:"title,omitempty"`
}

// MetadataPath returns the path to wt.json for a worktree.
// Git stores worktree metadata at <repo>/.git/worktrees/<name>/
func MetadataPath(repo, name string) string {
	return filepath.Join(repo, ".git", "worktrees", name, "wt.json")
}

// ReadMetadata reads the metadata for a worktree. Returns zero value on any error.
func ReadMetadata(repo, name string) Metadata {
	path := MetadataPath(repo, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return Metadata{}
	}
	var m Metadata
	json.Unmarshal(data, &m)
	return m
}

// WriteMetadata writes metadata for a worktree.
func WriteMetadata(repo, name string, m Metadata) error {
	path := MetadataPath(repo, name)
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
