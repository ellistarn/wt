package discover

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ellistarn/wt/pkg/ssh"
	"github.com/ellistarn/wt/pkg/worktree"
)

// ListRemote finds all worktrees on the remote host.
func ListRemote(host string) []worktree.Entry {
	script := `
set -eu
home=$(readlink -f "$HOME")
shopt -s nullglob
for wt_dir in "$home"/*/.worktrees "$home"/*/*/.worktrees "$home"/*/*/*/.worktrees "$home"/*/*/*/*/.worktrees "$home"/*/*/*/*/*/.worktrees; do
    repo="${wt_dir%/.worktrees}"
    if [ -d "$repo/.git" ] || [ -f "$repo/.git" ]; then
        git -C "$repo" worktree list --porcelain 2>/dev/null | awk -v repo="$repo" '
            /^worktree / { wt=$2 }
            /^branch / {
                br=$2; sub(/^refs\/heads\//, "", br)
                if (wt ~ /\/.worktrees\//) print wt "\t" br "\t" repo
            }
        '
    fi
done
`
	out, err := ssh.Run(host, script)
	if err != nil {
		return nil
	}

	// Collect worktree paths to batch-stat their .git files for creation time
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return nil
	}

	// Build a batch stat command for all .git files
	var statPaths []string
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) >= 1 {
			statPaths = append(statPaths, fmt.Sprintf("'%s/.git'", parts[0]))
		}
	}
	statScript := fmt.Sprintf("stat -c '%%Y' %s 2>/dev/null || true", strings.Join(statPaths, " "))
	statOut, _ := ssh.Run(host, statScript)
	statLines := strings.Split(strings.TrimSpace(statOut), "\n")

	var entries []worktree.Entry
	for i, line := range lines {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		e := worktree.Entry{
			Dir:    parts[0],
			Name:   parts[1],
			Repo:   parts[2],
			Remote: true,
		}
		if i < len(statLines) {
			if ts, err := strconv.ParseInt(strings.TrimSpace(statLines[i]), 10, 64); err == nil {
				e.CreatedAt = worktree.TimeUnix(ts)
			}
		}
		entries = append(entries, e)
	}
	return entries
}
