package git

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/ellistarn/wt/pkg/ssh"
)

// DiffStat returns a --stat summary of changes on this branch vs the merge-base
// with the root branch's upstream ref. Includes untracked files. Returns "" if
// there are no changes.
func DiffStat(host, dir, repo string) (string, error) {
	upstream, err := UpstreamRef(host, repo)
	if err != nil {
		return "", err
	}
	if host != "" {
		script := fmt.Sprintf(
			`mb=$(git -C '%s' merge-base '%s' HEAD) || exit 1
tracked=$(git -C '%s' diff --stat "$mb")
untracked=""
git -C '%s' ls-files --others --exclude-standard | while IFS= read -r f; do
    [ -z "$f" ] && continue
    line=$(cd '%s' && git diff --stat --no-index -- /dev/null "$f" 2>/dev/null | head -1) || true
    [ -n "$line" ] && printf '%%s\n' "$line"
done > /tmp/wt_untracked_stat_$$
untracked=$(cat /tmp/wt_untracked_stat_$$ 2>/dev/null)
rm -f /tmp/wt_untracked_stat_$$
if [ -n "$tracked" ] && [ -n "$untracked" ]; then
    printf '%%s\n%%s\n' "$tracked" "$untracked"
elif [ -n "$tracked" ]; then
    printf '%%s\n' "$tracked"
elif [ -n "$untracked" ]; then
    printf '%%s\n' "$untracked"
fi`,
			dir, upstream, dir, dir, dir,
		)
		out, err := ssh.Run(host, script)
		return strings.TrimSpace(out), err
	}
	mb, err := runGit("", dir, "merge-base", upstream, "HEAD")
	if err != nil {
		return "", fmt.Errorf("merge-base: %w", err)
	}
	tracked, _ := runGit("", dir, "diff", "--stat", mb)

	// Append untracked file stats
	untracked := untrackedFiles("", dir)
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

// Diff returns the full diff of changes on this branch vs the merge-base
// with the root branch's upstream ref. Includes untracked files as new file
// diffs. If color is true, ANSI color codes are included.
func Diff(host, dir, repo string, color bool) (string, error) {
	upstream, err := UpstreamRef(host, repo)
	if err != nil {
		return "", err
	}
	colorFlag := "--color=never"
	if color {
		colorFlag = "--color=always"
	}
	if host != "" {
		script := fmt.Sprintf(
			`mb=$(git -C '%s' merge-base '%s' HEAD) || exit 1
git -C '%s' diff '%s' "$mb"
git -C '%s' ls-files --others --exclude-standard | while IFS= read -r f; do
    [ -z "$f" ] && continue
    (cd '%s' && git diff '%s' --no-index -- /dev/null "$f" 2>/dev/null) || true
done`,
			dir, upstream, dir, colorFlag, dir, dir, colorFlag,
		)
		out, err := ssh.Run(host, script)
		return strings.TrimSpace(out), err
	}
	mb, err := runGit("", dir, "merge-base", upstream, "HEAD")
	if err != nil {
		return "", fmt.Errorf("merge-base: %w", err)
	}
	tracked, _ := runGit("", dir, "diff", colorFlag, mb)

	// Append untracked file diffs
	untracked := untrackedFiles("", dir)
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

// HasDiff reports whether the worktree has any visible changes — tracked
// modifications relative to the merge-base OR untracked files. This is the
// single source of truth for "are there changes worth showing/protecting."
// It uses the same signals as DiffStat: merge-base diff + untracked files.
func HasDiff(host, dir, repo string) bool {
	upstream, err := UpstreamRef(host, repo)
	if err != nil {
		// Can't determine upstream; conservatively check for untracked files.
		return len(untrackedFiles(host, dir)) > 0
	}
	mb, err := runGit(host, dir, "merge-base", upstream, "HEAD")
	if err != nil {
		return len(untrackedFiles(host, dir)) > 0
	}
	// Use --quiet for efficiency: exits 1 if differences exist.
	_, err = runGit(host, dir, "diff", "--quiet", mb)
	if err != nil {
		return true // tracked changes exist
	}
	return len(untrackedFiles(host, dir)) > 0
}

// HasUncommittedChanges reports whether the worktree has modifications not
// yet committed — tracked file changes relative to HEAD or untracked files.
// Used as a safety check when a merged branch still shows a diff (to
// distinguish squash-merge artifacts from new uncommitted work).
func HasUncommittedChanges(host, dir string) bool {
	// git diff --quiet HEAD exits 1 if working tree differs from HEAD.
	_, err := runGit(host, dir, "diff", "--quiet", "HEAD")
	if err != nil {
		return true
	}
	return len(untrackedFiles(host, dir)) > 0
}

// untrackedFiles returns untracked file paths relative to the worktree root.
func untrackedFiles(host, dir string) []string {
	out, err := runGit(host, dir, "ls-files", "--others", "--exclude-standard")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// diffNoIndexStat returns the first line of --stat output for an untracked file.
func diffNoIndexStat(dir, file string) string {
	cmd := exec.Command("git", "diff", "--stat", "--no-index", "--", "/dev/null", file)
	cmd.Dir = dir
	out, _ := cmd.Output() // exit code 1 is expected (differences found)
	lines := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	if len(lines) > 0 && lines[0] != "" {
		return lines[0]
	}
	return ""
}

// diffNoIndex returns a full unified diff for an untracked file (shown as new).
func diffNoIndex(dir, file string, color bool) string {
	colorFlag := "--color=never"
	if color {
		colorFlag = "--color=always"
	}
	cmd := exec.Command("git", "diff", colorFlag, "--no-index", "--", "/dev/null", file)
	cmd.Dir = dir
	out, _ := cmd.Output() // exit code 1 is expected (differences found)
	return strings.TrimSpace(string(out))
}
