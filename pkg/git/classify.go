package git

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ellistarn/wt/pkg/ssh"
)

// IsClean returns true if the worktree has no modified, staged, or untracked files.
func IsClean(host, dir string) bool {
	out, err := runGit(host, dir, "status", "--porcelain")
	return err == nil && out == ""
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
