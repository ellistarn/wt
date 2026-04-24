package discover

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

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
	// The callback is thread-safe because the walk parallelizes at depth 0.
	var repos []string
	seen := make(map[string]bool)
	var repoMu sync.Mutex
	findWorktreeDirs(home, 0, 10, func(repo string) {
		repoMu.Lock()
		defer repoMu.Unlock()
		if !seen[repo] {
			seen[repo] = true
			repos = append(repos, repo)
		}
	})

	// Query git in parallel across repos.
	var mu sync.Mutex
	var all []worktree.Entry
	var wg sync.WaitGroup
	for _, repo := range repos {
		wg.Add(1)
		go func(r string) {
			defer wg.Done()
			entries := listInRepo(r)
			mu.Lock()
			all = append(all, entries...)
			mu.Unlock()
		}(repo)
	}
	wg.Wait()
	return all
}

// findWorktreeDirs walks directories looking for .worktrees entries.
// Uses three generic pruning strategies to stay fast on any filesystem:
//  1. Hidden directories (starting with ".") are skipped.
//  2. Git repo roots (.git detected) are leaf nodes — their children are
//     source code, not nested repos. The walk root (depth 0) is exempt
//     because $HOME is commonly a dotfiles repo containing real code repos.
//  3. Directories with >100 children are skipped. Code-organizational
//     directories (go/src/, github.com/org/) have low fan-out; only
//     caches and artifact stores (Go module cache, node_modules) have
//     hundreds of siblings.
func findWorktreeDirs(dir string, depth, maxDepth int, fn func(repo string)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	hasGit := false
	var children []string
	for _, e := range entries {
		name := e.Name()
		if name == ".git" {
			hasGit = true
			continue
		}
		if !e.IsDir() {
			continue
		}
		if name == ".worktrees" {
			fn(dir)
			continue
		}
		if !strings.HasPrefix(name, ".") {
			children = append(children, name)
		}
	}

	if hasGit && depth > 0 {
		return
	}
	if len(children) > 100 {
		return
	}
	// Parallelize at the walk root — each top-level directory under $HOME
	// gets its own goroutine so heavy subtrees don't block the rest.
	if depth == 0 {
		var wg sync.WaitGroup
		for _, name := range children {
			wg.Add(1)
			go func(n string) {
				defer wg.Done()
				findWorktreeDirs(filepath.Join(dir, n), depth+1, maxDepth, fn)
			}(name)
		}
		wg.Wait()
		return
	}
	for _, name := range children {
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
			if strings.HasPrefix(currentWT, wtPrefix) {
				e := worktree.Entry{
					Name: strings.TrimPrefix(currentWT, wtPrefix),
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
