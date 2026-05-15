package provider

import (
	"os"
	"path/filepath"
	"time"
)

func queryClaude(dir string) SessionInfo {
	// Check for .claude/ directory in the worktree itself.
	// Use most recent file mtime as activity indicator.
	claudeDir := filepath.Join(dir, ".claude")
	if _, err := os.Stat(claudeDir); err != nil {
		return SessionInfo{}
	}

	var latest time.Time
	filepath.WalkDir(claudeDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})

	if latest.IsZero() {
		return SessionInfo{}
	}

	return SessionInfo{
		Activity: latest,
	}
}
