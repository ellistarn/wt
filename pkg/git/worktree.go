package git

import (
	"fmt"
	"os"
)

// WorktreeAdd creates a new worktree at wtDir on branch <name>.
func WorktreeAdd(repo, name, wtDir string) error {
	args := []string{"worktree", "add", wtDir, "-b", name}
	out, err := runCapture(repo, args...)
	if err != nil {
		return err
	}
	logCmd(repo, out, args...)
	return nil
}

// Pull fetches with prune and fast-forwards the current branch.
func Pull(repo string) error {
	args := []string{"pull", "--ff-only", "--prune"}
	out, err := runCapture(repo, args...)
	if err != nil {
		return err
	}
	logCmd(repo, out, args...)
	return nil
}

// WorktreeRemove removes the worktree directory and deletes the branch.
func WorktreeRemove(repo, branch, wtDir string) error {
	removeWorktreeAndBranch(repo, branch, wtDir, false)
	return nil
}

// WorktreeForceRemove removes the worktree and branch without safety checks.
func WorktreeForceRemove(repo, branch, wtDir string) error {
	removeWorktreeAndBranch(repo, branch, wtDir, true)
	return nil
}

func removeWorktreeAndBranch(repo, branch, wtDir string, force bool) {
	args := []string{"worktree", "remove", wtDir}
	if force {
		args = []string{"worktree", "remove", "--force", wtDir}
	}
	out, err := runCapture(repo, args...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: git worktree remove failed for %s: %v\n", wtDir, err)
		pruneArgs := []string{"worktree", "prune"}
		pruneOut, _ := runCapture(repo, pruneArgs...)
		logCmd(repo, pruneOut, pruneArgs...)
	} else {
		logCmd(repo, out, args...)
	}

	if branch != "" {
		branchArgs := []string{"branch", "-D", branch}
		out, err = runGit(repo, branchArgs...)
		logCmd(repo, out, branchArgs...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: branch delete failed for %s: %v\n", branch, err)
		}
	}
}
