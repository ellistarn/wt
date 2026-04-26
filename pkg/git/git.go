package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ellistarn/wt/pkg/display"
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

// UpstreamRef returns the upstream tracking ref for the given branch
// (e.g., "origin/krocodile"). Returns an error if no upstream is configured.
// Works from any directory in the repo (worktree or root).
func UpstreamRef(host, dir, branch string) (string, error) {
	out, err := runGit(host, dir, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+branch)
	if err != nil || out == "" {
		return "", fmt.Errorf("no upstream configured for branch %q\n\nSet it with: git branch --set-upstream-to=origin/<base> %s", branch, branch)
	}
	return out, nil
}

// UniqueCommitCount returns the number of commits on branch that are not on
// its upstream tracking ref. Returns 0 if the branch has not diverged or has
// no upstream configured.
func UniqueCommitCount(host, repo, branch string) int {
	upstream, err := UpstreamRef(host, repo, branch)
	if err != nil {
		return 0
	}
	out, err := runGit(host, repo, "rev-list", "--count", upstream+".."+branch)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(out)
	return n
}

// IsMerged returns true if the branch's changes are incorporated into
// its upstream tracking ref (regular merge, fast-forward, or squash merge).
//
// Detection is three-phase:
//  1. Ancestry check — catches regular merges and fast-forward merges.
//  2. Merge-tree simulation — catches squash merges when the branch merges
//     cleanly into the upstream. Requires git 2.38+.
//  3. Patch-id comparison — catches squash merges when merge-tree produces
//     conflicts (e.g., when the upstream has moved forward significantly).
//     Computes the branch's aggregate diff patch-id and searches for a commit
//     on the upstream with a matching patch-id. Works for single and
//     multi-commit squash merges.
//
// Callers should only invoke this when the branch has unique commits
// (UniqueCommitCount > 0). A branch with no divergence trivially matches
// the target tree and would produce a false positive.
func IsMerged(host, repo, branch string) bool {
	upstream, err := UpstreamRef(host, repo, branch)
	if err != nil {
		return false
	}
	target := upstream

	// Phase 1: ancestry check (regular merge / fast-forward).
	if _, err := runGit(host, repo, "merge-base", "--is-ancestor", branch, target); err == nil {
		return true
	}

	// Phase 2: merge-tree simulation (squash merge, no conflicts).
	mergeTree, err := runGit(host, repo, "merge-tree", "--write-tree", target, branch)
	if err == nil {
		targetTree, err := runGit(host, repo, "rev-parse", target+"^{tree}")
		if err == nil && mergeTree == targetTree {
			return true
		}
	}

	// Phase 3: patch-id comparison (squash merge, merge-tree had conflicts).
	return hasPatchIDMatch(host, repo, target, branch)
}

// hasPatchIDMatch computes the aggregate patch-id of the branch's diff
// (merge-base to branch tip) and checks whether any commit on the target
// has the same patch-id. This detects squash merges even when merge-tree
// simulation fails due to conflicts with later changes on the target.
// Works for both single-commit and multi-commit squash merges.
func hasPatchIDMatch(host, repo, target, branch string) bool {
	mergeBase, err := runGit(host, repo, "merge-base", target, branch)
	if err != nil {
		return false
	}
	diff, err := runGit(host, repo, "diff", mergeBase, branch)
	if err != nil || diff == "" {
		return false
	}
	branchPID := computePatchID(diff)
	if branchPID == "" {
		return false
	}
	return searchPatchID(repo, mergeBase+".."+target, branchPID)
}

// computePatchID computes the patch-id of a diff by piping it through
// git patch-id --stable.
func computePatchID(diff string) string {
	cmd := exec.Command("git", "patch-id", "--stable")
	cmd.Stdin = strings.NewReader(diff)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	if fields := strings.Fields(strings.TrimSpace(string(out))); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// searchPatchID pipes git log -p through git patch-id --stable and checks
// whether any commit in the range has the given patch-id. Searches at most
// 500 commits to bound cost on repos with long histories.
func searchPatchID(repo, revRange, targetPID string) bool {
	logCmd := exec.Command("git", "-C", repo, "log", "-p", "--max-count=500", revRange)
	pidCmd := exec.Command("git", "patch-id", "--stable")

	pipe, err := logCmd.StdoutPipe()
	if err != nil {
		return false
	}
	pidCmd.Stdin = pipe

	var out bytes.Buffer
	pidCmd.Stdout = &out

	if err := logCmd.Start(); err != nil {
		return false
	}
	if err := pidCmd.Start(); err != nil {
		logCmd.Process.Kill()
		return false
	}
	logCmd.Wait()
	pidCmd.Wait()

	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == targetPID {
			return true
		}
	}
	return false
}

// ClassifyEntry holds the input for batch classification.
type ClassifyEntry struct {
	Dir    string // worktree directory
	Repo   string // repo root
	Branch string // branch name
}

// ClassifyResult holds the git classification for a single worktree.
type ClassifyResult struct {
	Clean  bool // no modified, staged, or untracked files
	Unique int  // commits on branch not on its upstream
	Merged bool // branch changes incorporated into its upstream
}

// ClassifyBatch classifies multiple remote worktrees in a single SSH call.
// Replicates the logic of IsClean + UniqueCommitCount + IsMerged but runs
// all git commands on the remote host in one round-trip.
// Returns an error if the SSH call fails entirely.
func ClassifyBatch(host string, entries []ClassifyEntry) ([]ClassifyResult, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	// Build heredoc with one entry per line: dir\trepo\tbranch
	var heredoc strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&heredoc, "%s\t%s\t%s\n", e.Dir, e.Repo, e.Branch)
	}

	script := `
set -eu

while IFS=$'\t' read -r dir repo branch; do
    [ -z "$dir" ] && continue

    # IsClean
    status=$(git -C "$dir" status --porcelain 2>/dev/null) || status=""
    if [ -n "$status" ]; then
        clean=false
    else
        clean=true
    fi

    # Get upstream tracking ref for this branch
    upstream=$(git -C "$repo" for-each-ref --format='%(upstream:short)' "refs/heads/$branch" 2>/dev/null)
    if [ -z "$upstream" ]; then
        printf '%s\t%s\t%s\n' "$clean" "0" "false"
        continue
    fi

    # UniqueCommitCount
    unique=$(git -C "$repo" rev-list --count "$upstream..$branch" 2>/dev/null) || unique=0

    # IsMerged (only if unique > 0)
    merged=false
    if [ "$unique" -gt 0 ] 2>/dev/null; then
        if git -C "$repo" merge-base --is-ancestor "$branch" "$upstream" 2>/dev/null; then
            merged=true
        else
            merge_tree=$(git -C "$repo" merge-tree --write-tree "$upstream" "$branch" 2>/dev/null) || merge_tree=""
            target_tree=$(git -C "$repo" rev-parse "${upstream}^{tree}" 2>/dev/null) || target_tree=""
            if [ -n "$merge_tree" ] && [ "$merge_tree" = "$target_tree" ]; then
                merged=true
            fi
            # Phase 3: patch-id comparison (squash merge, merge-tree had conflicts)
            if [ "$merged" = "false" ]; then
                mb=$(git -C "$repo" merge-base "$upstream" "$branch" 2>/dev/null) || mb=""
                if [ -n "$mb" ]; then
                    bpid=$(git -C "$repo" diff "$mb" "$branch" 2>/dev/null | git patch-id --stable 2>/dev/null | awk '{print $1}')
                    if [ -n "$bpid" ]; then
                        match=$(git -C "$repo" log -p --max-count=500 "$mb..$upstream" 2>/dev/null | git patch-id --stable 2>/dev/null | awk -v pid="$bpid" '$1 == pid {print "yes"; exit}')
                        if [ "$match" = "yes" ]; then
                            merged=true
                        fi
                    fi
                fi
            fi
        fi
    fi

    printf '%s\t%s\t%s\n' "$clean" "$unique" "$merged"
done << 'ENTRIES'
` + heredoc.String() + `ENTRIES
`
	out, err := ssh.Run(host, script)
	if err != nil {
		return nil, fmt.Errorf("remote classify: %w", err)
	}

	results := make([]ClassifyResult, len(entries))
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i, line := range lines {
		if i >= len(entries) {
			break
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		results[i].Clean = parts[0] == "true"
		results[i].Unique, _ = strconv.Atoi(parts[1])
		results[i].Merged = parts[2] == "true"
	}
	return results, nil
}

// DiffStat returns a --stat summary of changes on this branch vs the merge-base
// with its upstream tracking ref. Returns "" if there are no changes.
func DiffStat(host, dir string) (string, error) {
	branch, err := runGit(host, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("cannot determine branch: %w", err)
	}
	upstream, err := UpstreamRef(host, dir, branch)
	if err != nil {
		return "", err
	}
	if host != "" {
		script := fmt.Sprintf(
			`mb=$(git -C '%s' merge-base '%s' HEAD) && git -C '%s' diff --stat "$mb"`,
			dir, upstream, dir,
		)
		out, err := ssh.Run(host, script)
		return strings.TrimSpace(out), err
	}
	mb, err := runGit("", dir, "merge-base", upstream, "HEAD")
	if err != nil {
		return "", fmt.Errorf("merge-base: %w", err)
	}
	return runGit("", dir, "diff", "--stat", mb)
}

// Diff returns the full diff of changes on this branch vs the merge-base
// with its upstream tracking ref. If color is true, ANSI color codes are included.
func Diff(host, dir string, color bool) (string, error) {
	branch, err := runGit(host, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("cannot determine branch: %w", err)
	}
	upstream, err := UpstreamRef(host, dir, branch)
	if err != nil {
		return "", err
	}
	colorFlag := "--color=never"
	if color {
		colorFlag = "--color=always"
	}
	if host != "" {
		script := fmt.Sprintf(
			`mb=$(git -C '%s' merge-base '%s' HEAD) && git -C '%s' diff '%s' "$mb"`,
			dir, upstream, dir, colorFlag,
		)
		out, err := ssh.Run(host, script)
		return strings.TrimSpace(out), err
	}
	mb, err := runGit("", dir, "merge-base", upstream, "HEAD")
	if err != nil {
		return "", fmt.Errorf("merge-base: %w", err)
	}
	return runGit("", dir, "diff", colorFlag, mb)
}

// IsClean returns true if the worktree has no modified, staged, or untracked files.
func IsClean(host, dir string) bool {
	out, err := runGit(host, dir, "status", "--porcelain")
	return err == nil && out == ""
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
	display.LogCmd(cmd)
	display.LogOutput(output)
}
