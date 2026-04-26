package git

import "fmt"

// WorktreeAdd creates a new worktree at <repo>/.worktrees/<name> on branch <name>.
// Sets the new branch's upstream tracking ref to origin/<root-branch>, where
// root-branch is whatever the repo root has checked out.
func WorktreeAdd(host, repo, name string) error {
	args := []string{"worktree", "add", ".worktrees/" + name, "-b", name}
	out, err := runCapture(host, repo, args...)
	if err != nil {
		return err
	}
	logCmd(host, repo, out, args...)

	// Determine the root branch (what the repo root has checked out)
	rootBranch, err := runGit(host, repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("cannot determine root branch: %w", err)
	}
	// Set upstream so diff/ls know what to compare against
	if _, err := runGit(host, repo, "branch", "--set-upstream-to", "origin/"+rootBranch, name); err != nil {
		return fmt.Errorf("cannot set upstream for %s: %w", name, err)
	}
	return nil
}

// Pull fetches with prune and fast-forwards the current branch.
// Uses --ff-only to fail explicitly if the local branch has diverged.
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
