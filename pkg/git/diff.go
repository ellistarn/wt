package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// DiffStat returns a --stat summary of changes on this branch vs the merge-base
// with the root branch's upstream ref. Includes untracked files.
func DiffStat(dir, repo string) (string, error) {
	upstream, err := UpstreamRef(repo)
	if err != nil {
		return "", err
	}
	mb, err := runGit(dir, "merge-base", upstream, "HEAD")
	if err != nil {
		return "", fmt.Errorf("merge-base: %w", err)
	}
	tracked, _ := runGit(dir, "diff", "--stat", mb)

	untracked := untrackedFiles(dir)
	if len(untracked) == 0 {
		return tracked, nil
	}
	var parts []string
	if tracked != "" {
		parts = append(parts, tracked)
	}
	for _, f := range untracked {
		line := diffNoIndexStat(dir, f)
		if line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// Diff returns the full diff of changes on this branch vs the merge-base.
func Diff(dir, repo string, color bool) (string, error) {
	upstream, err := UpstreamRef(repo)
	if err != nil {
		return "", err
	}
	colorFlag := "--color=never"
	if color {
		colorFlag = "--color=always"
	}
	mb, err := runGit(dir, "merge-base", upstream, "HEAD")
	if err != nil {
		return "", fmt.Errorf("merge-base: %w", err)
	}
	tracked, _ := runGit(dir, "diff", colorFlag, mb)

	untracked := untrackedFiles(dir)
	if len(untracked) == 0 {
		return tracked, nil
	}
	var parts []string
	if tracked != "" {
		parts = append(parts, tracked)
	}
	for _, f := range untracked {
		d := diffNoIndex(dir, f, color)
		if d != "" {
			parts = append(parts, d)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// HasDiff reports whether the worktree has any visible changes.
func HasDiff(dir, repo string) bool {
	upstream, err := UpstreamRef(repo)
	if err != nil {
		return len(untrackedFiles(dir)) > 0
	}
	mb, err := runGit(dir, "merge-base", upstream, "HEAD")
	if err != nil {
		return len(untrackedFiles(dir)) > 0
	}
	_, err = runGit(dir, "diff", "--quiet", mb)
	if err != nil {
		return true
	}
	return len(untrackedFiles(dir)) > 0
}

// HasUncommittedChanges reports whether the worktree has modifications not
// yet committed.
func HasUncommittedChanges(dir string) bool {
	_, err := runGit(dir, "diff", "--quiet", "HEAD")
	if err != nil {
		return true
	}
	return len(untrackedFiles(dir)) > 0
}

// untrackedFiles returns untracked file paths relative to the worktree root.
func untrackedFiles(dir string) []string {
	out, err := runGit(dir, "ls-files", "--others", "--exclude-standard")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// diffNoIndexStat returns the first line of --stat output for an untracked file.
func diffNoIndexStat(dir, file string) string {
	cmd := exec.Command("git", "diff", "--stat", "--no-index", "--", "/dev/null", file)
	cmd.Dir = dir
	out, _ := cmd.Output()
	lines := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	if len(lines) > 0 && lines[0] != "" {
		return lines[0]
	}
	return ""
}

// diffNoIndex returns a full unified diff for an untracked file.
func diffNoIndex(dir, file string, color bool) string {
	colorFlag := "--color=never"
	if color {
		colorFlag = "--color=always"
	}
	cmd := exec.Command("git", "diff", colorFlag, "--no-index", "--", "/dev/null", file)
	cmd.Dir = dir
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}
