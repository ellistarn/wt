package discover

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ellistarn/wt/pkg/worktree"
)

// ListLocal finds all worktrees under the user's home directory.
func ListLocal() []worktree.Entry {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}

	// Find .worktrees directories by walking with os.ReadDir, which uses
	// the d_type field from readdir to check IsDir() without stat syscalls.
	var all []worktree.Entry
	seen := make(map[string]bool)
	findWorktreeDirs(home, 0, 6, func(repo string) {
		if seen[repo] {
			return
		}
		seen[repo] = true
		all = append(all, listInRepo(repo)...)
	})
	return all
}

// findWorktreeDirs walks directories looking for .worktrees entries.
// Uses os.ReadDir which returns d_type from readdir (no stat per entry).
func findWorktreeDirs(dir string, depth, maxDepth int, fn func(repo string)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == ".worktrees" {
			fn(dir)
			continue
		}
		// Skip hidden directories and common heavy dirs
		if strings.HasPrefix(name, ".") {
			continue
		}
		if depth < maxDepth {
			findWorktreeDirs(filepath.Join(dir, name), depth+1, maxDepth, fn)
		}
	}
}

// listInRepo lists worktrees within a single local repo.
func listInRepo(repo string) []worktree.Entry {
	b, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil
	}
	return parseWorktreeList(string(b), repo)
}

func parseWorktreeList(porcelain, repo string) []worktree.Entry {
	var entries []worktree.Entry
	var currentWT string
	wtPrefix := repo + "/.worktrees/"

	for _, line := range strings.Split(porcelain, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			currentWT = strings.TrimPrefix(line, "worktree ")
		}
		if strings.HasPrefix(line, "branch ") && currentWT != "" {
			branch := strings.TrimPrefix(line, "branch refs/heads/")
			if strings.HasPrefix(currentWT, wtPrefix) {
				e := worktree.Entry{
					Name: branch,
					Dir:  currentWT,
					Repo: repo,
				}
				// Stat the .git file to get creation time.
				// Git creates this file when the worktree is added; it doesn't change.
				if info, err := os.Stat(filepath.Join(currentWT, ".git")); err == nil {
					e.CreatedAt = info.ModTime()
				}
				entries = append(entries, e)
			}
			currentWT = ""
		}
	}
	return entries
}
