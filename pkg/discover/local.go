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
	var repos []string
	seen := make(map[string]bool)
	var repoMu sync.Mutex
	findWorktreeDirs(home, 10, 16, func(repo string) {
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
//
// A fixed pool of workers processes a directory queue so wall time scales
// with tree depth rather than total directory count, without goroutine
// explosion.
func findWorktreeDirs(root string, maxDepth, workers int, fn func(repo string)) {
	type item struct {
		dir   string
		depth int
	}

	var mu sync.Mutex
	queue := []item{{root, 0}}
	active := 0
	wake := sync.NewCond(&mu)

	for i := 0; i < workers; i++ {
		go func() {
			for {
				mu.Lock()
				for len(queue) == 0 && active > 0 {
					wake.Wait()
				}
				if len(queue) == 0 {
					mu.Unlock()
					wake.Broadcast()
					return
				}
				it := queue[0]
				queue = queue[1:]
				active++
				mu.Unlock()

				children := walkDir(it.dir, it.depth, fn)
				if it.depth < maxDepth {
					mu.Lock()
					for _, name := range children {
						queue = append(queue, item{filepath.Join(it.dir, name), it.depth + 1})
					}
					active--
					mu.Unlock()
					wake.Broadcast()
				} else {
					mu.Lock()
					active--
					mu.Unlock()
					wake.Broadcast()
				}
			}
		}()
	}

	// Wait for all workers to finish.
	mu.Lock()
	for active > 0 || len(queue) > 0 {
		wake.Wait()
	}
	mu.Unlock()
	wake.Broadcast()
}

// walkDir reads one directory and returns the child directory names to recurse
// into after applying pruning rules. Found repos are reported via fn.
func walkDir(dir string, depth int, fn func(repo string)) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
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
		return nil
	}
	if len(children) > 100 {
		return nil
	}
	return children
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
