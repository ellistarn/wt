package git

import (
	"fmt"
	"os"
)

// WorktreeAdd creates a new worktree at wtDir on branch <name>. Sets the new
// branch's upstream tracking ref to origin/<root-branch>, where root-branch is
// whatever the repo root has checked out.
func WorktreeAdd(host, repo, name, wtDir string) error {
	args := []string{"worktree", "add", wtDir, "-b", name}
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

// WorktreeRemove removes the worktree directory and force-deletes the branch.
// wtDir is the worktree's actual path on disk (may be sibling or child layout).
// The caller's classification logic has already confirmed safety.
func WorktreeRemove(host, repo, name, wtDir string) error {
	removeWorktreeAndBranch(host, repo, name, wtDir, false)
	return nil
}

// WorktreeForceRemove removes the worktree and branch without safety checks.
// wtDir is the worktree's actual path on disk (may be sibling or child layout).
func WorktreeForceRemove(host, repo, name, wtDir string) error {
	removeWorktreeAndBranch(host, repo, name, wtDir, true)
	return nil
}

// removeWorktreeAndBranch deregisters the worktree and deletes its branch.
// The two operations are independent — a missing worktree directory must not
// prevent the branch from being cleaned up.
func removeWorktreeAndBranch(host, repo, name, wtDir string, force bool) {
	// Step 1: try to deregister the worktree directory.
	args := []string{"worktree", "remove", wtDir}
	if force {
		args = []string{"worktree", "remove", "--force", wtDir}
	}
	out, err := runCapture(host, repo, args...)
	if err != nil {
		// Directory already gone or other issue — prune stale registrations
		// so git no longer thinks the branch is checked out.
		fmt.Fprintf(os.Stderr, "warning: git worktree remove failed for %s: %v\n", name, err)
		pruneArgs := []string{"worktree", "prune"}
		pruneOut, _ := runCapture(host, repo, pruneArgs...)
		logCmd(host, repo, pruneOut, pruneArgs...)
	} else {
		logCmd(host, repo, out, args...)
	}

	// Step 2: always delete the branch, regardless of step 1's outcome.
	branchArgs := []string{"branch", "-D", name}
	out, err = runGit(host, repo, branchArgs...)
	logCmd(host, repo, out, branchArgs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: branch delete failed for %s: %v\n", name, err)
	}
}
