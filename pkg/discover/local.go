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

	// Find git repos by walking for .git directories. Uses os.ReadDir
	// which reads d_type from readdir to check IsDir() without stat syscalls.
	var repos []string
	seen := make(map[string]bool)
	var repoMu sync.Mutex
	findGitRepos(home, 10, 16, func(repo string) {
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

// findGitRepos walks directories looking for .git entries (directories or files).
// A .git directory indicates a repo root; a .git file indicates a worktree
// checkout (which is found via its parent repo's git worktree list).
//
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
func findGitRepos(root string, maxDepth, workers int, fn func(repo string)) {
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
		if !strings.HasPrefix(name, ".") {
			children = append(children, name)
		}
	}

	if hasGit {
		fn(dir)
		if depth > 0 {
			return nil
		}
	}

	if len(children) > 100 {
		return nil
	}
	return children
}

// listInRepo lists worktrees within a single local repo.
// Skips worktree checkouts (.git is a file) to avoid duplicate discovery —
// git worktree list returns the same results from any worktree of a repo.
func listInRepo(repo string) []worktree.Entry {
	info, err := os.Lstat(filepath.Join(repo, ".git"))
	if err != nil || !info.IsDir() {
		return nil // .git file = worktree checkout, skip
	}
	b, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil
	}
	return parseWorktreeList(string(b), repo)
}

func parseWorktreeList(porcelain, repo string) []worktree.Entry {
	var entries []worktree.Entry
	var currentWT string
	var currentBranch string

	for _, line := range strings.Split(porcelain, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			currentWT = strings.TrimPrefix(line, "worktree ")
		}
		if strings.HasPrefix(line, "branch ") {
			currentBranch = strings.TrimPrefix(line, "branch ")
			// Strip refs/heads/ prefix
			currentBranch = strings.TrimPrefix(currentBranch, "refs/heads/")
		}
		if line == "" && currentWT != "" {
			if e, ok := matchWorktree(currentWT, currentBranch, repo); ok {
				entries = append(entries, e)
			}
			currentWT = ""
			currentBranch = ""
		}
	}
	// Handle final block if porcelain doesn't end with a blank line.
	if currentWT != "" {
		if e, ok := matchWorktree(currentWT, currentBranch, repo); ok {
			entries = append(entries, e)
		}
	}
	return entries
}

// matchWorktree checks if a worktree entry is a non-root worktree (i.e., not
// the repo's main checkout). Every non-root worktree reported by git worktree
// list is considered wt-managed — no path-pattern matching needed.
func matchWorktree(wtPath, branch, repo string) (worktree.Entry, bool) {
	if branch == "" || wtPath == repo {
		return worktree.Entry{}, false
	}
	return newEntry(branch, wtPath, repo), true
}

func newEntry(name, dir, repo string) worktree.Entry {
	e := worktree.Entry{Name: name, Dir: dir, Repo: repo}
	if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		e.CreatedAt = info.ModTime()
	}
	return e
}
