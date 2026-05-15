package git

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"
)

// UniqueCommitCount returns the number of commits on branch that are not on
// its upstream tracking ref.
func UniqueCommitCount(repo, branch string) int {
	upstream, err := UpstreamRef(repo)
	if err != nil {
		return 0
	}
	out, err := runGit(repo, "rev-list", "--count", upstream+".."+branch)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(out)
	return n
}

// IsBehindUpstream returns true if the branch tip is a proper ancestor of
// its upstream.
func IsBehindUpstream(repo, branch string) bool {
	upstream, err := UpstreamRef(repo)
	if err != nil {
		return false
	}
	branchRev, err := runGit(repo, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return false
	}
	upstreamRev, err := runGit(repo, "rev-parse", upstream)
	if err != nil {
		return false
	}
	if branchRev == upstreamRev {
		return false
	}
	_, err = runGit(repo, "merge-base", "--is-ancestor", branch, upstream)
	return err == nil
}

// IsMerged returns true if the branch's changes are incorporated into
// its upstream tracking ref.
func IsMerged(repo, branch string) bool {
	upstream, err := UpstreamRef(repo)
	if err != nil {
		return false
	}
	target := upstream

	// Phase 1: ancestry check.
	if _, err := runGit(repo, "merge-base", "--is-ancestor", branch, target); err == nil {
		return true
	}

	// Phase 2: merge-tree simulation.
	mergeTree, err := runGit(repo, "merge-tree", "--write-tree", target, branch)
	if err == nil {
		targetTree, err := runGit(repo, "rev-parse", target+"^{tree}")
		if err == nil && mergeTree == targetTree {
			return true
		}
	}

	// Phase 3: patch-id comparison.
	return hasPatchIDMatch(repo, target, branch)
}

func hasPatchIDMatch(repo, target, branch string) bool {
	mergeBase, err := runGit(repo, "merge-base", target, branch)
	if err != nil {
		return false
	}
	diff, err := runGit(repo, "diff", mergeBase, branch)
	if err != nil || diff == "" {
		return false
	}
	branchPID := computePatchID(diff)
	if branchPID == "" {
		return false
	}
	return searchPatchID(repo, mergeBase+".."+target, branchPID)
}

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
