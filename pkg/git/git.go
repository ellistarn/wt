package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ellistarn/wt/pkg/ssh"
)

// RepoRoot returns the repo root. If host is empty, runs locally.
// For remote, pass the remote directory as extra args.
func RepoRoot(host string, dir ...string) (string, error) {
	if host == "" {
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return "", err
		}
		root := strings.TrimSpace(string(out))
		// Resolve symlinks so paths match OpenCode session directories
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		return root, nil
	}
	d := "."
	if len(dir) > 0 {
		d = dir[0]
	}
	out, err := ssh.Run(host, fmt.Sprintf("git -C '%s' rev-parse --show-toplevel", d))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// WorktreeAdd creates a new worktree at <repo>/.worktrees/<name> on branch <name>.
func WorktreeAdd(host, repo, name string) error {
	args := []string{"worktree", "add", ".worktrees/" + name, "-b", name}
	out, err := runCapture(host, repo, args...)
	if err != nil {
		return err
	}
	logCmd(host, repo, out, args...)
	return nil
}

// DirExists checks whether a directory exists, locally or over SSH.
func DirExists(host, path string) bool {
	if host == "" {
		info, err := os.Stat(path)
		return err == nil && info.IsDir()
	}
	_, err := ssh.Run(host, fmt.Sprintf("test -d '%s'", path))
	return err == nil
}

// runGit runs a git command in the given directory. If host is empty, runs locally.
func runGit(host, dir string, args ...string) (string, error) {
	if host == "" {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", "'\\''") + "'"
	}
	script := fmt.Sprintf("git -C '%s' %s", dir, strings.Join(quoted, " "))
	out, err := ssh.Run(host, script)
	return strings.TrimSpace(out), err
}

// defaultBranchCache avoids redundant git calls for the same repo.
// Key: "host\x00repo", Value: branch name string.
var defaultBranchCache sync.Map

// DefaultBranch returns the default branch name (e.g., "main" or "master")
// by checking refs/remotes/origin/HEAD, then probing main and master.
// Results are cached per (host, repo) for the lifetime of the process.
func DefaultBranch(host, repo string) string {
	cacheKey := host + "\x00" + repo
	if v, ok := defaultBranchCache.Load(cacheKey); ok {
		return v.(string)
	}
	result := defaultBranchUncached(host, repo)
	defaultBranchCache.Store(cacheKey, result)
	return result
}

func defaultBranchUncached(host, repo string) string {
	out, err := runGit(host, repo, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		// refs/remotes/origin/main -> main
		parts := strings.Split(out, "/")
		return parts[len(parts)-1]
	}
	// Probe common names
	for _, name := range []string{"main", "master"} {
		if _, err := runGit(host, repo, "rev-parse", "--verify", "refs/remotes/origin/"+name); err == nil {
			return name
		}
	}
	return "main" // fallback
}

// UniqueCommitCount returns the number of commits on branch that are not on
// origin/<default>. Returns 0 if the branch has not diverged.
func UniqueCommitCount(host, repo, branch string) int {
	def := DefaultBranch(host, repo)
	out, err := runGit(host, repo, "rev-list", "--count", "origin/"+def+".."+branch)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(out)
	return n
}

// IsMerged returns true if the branch's changes are incorporated into
// origin/<default> (regular merge, fast-forward, or squash merge).
//
// Detection is two-phase:
//  1. Ancestry check — catches regular merges and fast-forward merges.
//  2. Merge-tree simulation — catches squash merges. Simulates merging the
//     branch into origin/<default> and checks whether the result tree is
//     identical to origin/<default>'s tree (i.e., the branch adds nothing new).
//     Requires git 2.38+.
//
// Callers should only invoke this when the branch has unique commits
// (UniqueCommitCount > 0). A branch with no divergence trivially matches
// the target tree and would produce a false positive.
func IsMerged(host, repo, branch string) bool {
	def := DefaultBranch(host, repo)
	target := "origin/" + def

	// Fast path: ancestry check (regular merge / fast-forward).
	if _, err := runGit(host, repo, "merge-base", "--is-ancestor", branch, target); err == nil {
		return true
	}

	// Slow path: merge-tree simulation (squash merge).
	mergeTree, err := runGit(host, repo, "merge-tree", "--write-tree", target, branch)
	if err != nil {
		return false // conflict or git too old — not merged
	}
	targetTree, err := runGit(host, repo, "rev-parse", target+"^{tree}")
	if err != nil {
		return false
	}
	return mergeTree == targetTree
}

// IsClean returns true if the worktree has no modified, staged, or untracked files.
func IsClean(host, dir string) bool {
	out, err := runGit(host, dir, "status", "--porcelain")
	return err == nil && out == ""
}

// Fetch updates remote tracking refs for a repo.
func Fetch(host, repo string) {
	args := []string{"fetch", "origin"}
	out, _ := runCapture(host, repo, args...)
	if out != "" {
		logCmd(host, repo, out, args...)
	}
}

// Pull fetches with prune and fast-forwards the current branch. Used before
// creating worktrees so they branch from the latest remote state. Uses
// --ff-only to fail explicitly if the local branch has diverged.
func Pull(host, repo string) error {
	args := []string{"pull", "--ff-only", "--prune"}
	out, err := runCapture(host, repo, args...)
	if err != nil {
		return err
	}
	logCmd(host, repo, out, args...)
	return nil
}

// WorktreeRemove removes the worktree directory and deletes the branch.
// git worktree remove deletes the directory; git branch -d deletes the branch
// (only if merged, safe delete).
func WorktreeRemove(host, repo, name string) error {
	wtPath := repo + "/.worktrees/" + name
	args := []string{"worktree", "remove", wtPath}
	out, err := runCapture(host, repo, args...)
	if err != nil {
		return fmt.Errorf("git worktree remove: %w", err)
	}
	logCmd(host, repo, out, args...)
	// Best-effort branch delete. -d is safe (refuses unmerged branches).
	// If it fails (branch doesn't exist, not merged), that's fine.
	runGit(host, repo, "branch", "-d", name)
	return nil
}

// WorktreeForceRemove removes the worktree and branch without safety checks.
func WorktreeForceRemove(host, repo, name string) error {
	wtPath := repo + "/.worktrees/" + name
	args := []string{"worktree", "remove", "--force", wtPath}
	out, err := runCapture(host, repo, args...)
	if err != nil {
		return fmt.Errorf("git worktree remove --force: %w", err)
	}
	logCmd(host, repo, out, args...)
	// Force delete the branch regardless of merge status.
	runGit(host, repo, "branch", "-D", name)
	return nil
}

// runCapture runs a git command capturing combined stdout+stderr.
// Used for side-effect commands where output indicates what changed.
func runCapture(host, dir string, args ...string) (string, error) {
	if host == "" {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		raw, err := cmd.CombinedOutput()
		out := strings.TrimSpace(string(raw))
		if err != nil {
			return out, fmt.Errorf("%w: %s", err, out)
		}
		return out, nil
	}
	return runGit(host, dir, args...)
}

// logCmd prints a git command and its output to stderr.
func logCmd(host, dir, output string, args ...string) {
	cmd := "git -C " + dir + " " + strings.Join(args, " ")
	if host != "" {
		cmd = host + ": " + cmd
	}
	if output != "" {
		fmt.Fprintf(os.Stderr, "%s\n%s\n", cmd, output)
	} else {
		fmt.Fprintf(os.Stderr, "%s\n", cmd)
	}
}
