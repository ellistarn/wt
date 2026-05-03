package git

import (
	"fmt"
	"strings"

	"github.com/ellistarn/wt/pkg/ssh"
)

// DiffStat returns a --stat summary of changes on this branch vs the merge-base
// with the root branch's upstream ref. Returns "" if there are no changes.
func DiffStat(host, dir, repo string) (string, error) {
	upstream, err := UpstreamRef(host, repo)
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
// with the root branch's upstream ref. If color is true, ANSI color codes are included.
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
