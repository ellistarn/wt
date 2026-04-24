package discover

import (
	"path"
	"strconv"
	"strings"

	"github.com/ellistarn/wt/pkg/ssh"
	"github.com/ellistarn/wt/pkg/worktree"
)

// ListRemote finds all worktrees on the remote host.
// Uses find to walk the home directory (no depth limit from hardcoded globs),
// prunes hidden dirs for speed, and collects worktree metadata including
// timestamps in a single SSH call.
func ListRemote(host string) []worktree.Entry {
	script := `
set -eu
home=$(readlink -f "$HOME")
find "$home" -maxdepth 10 -type d \( -name .worktrees -print -prune -o -name '.*' -prune \) | while IFS= read -r wt_dir; do
    repo="${wt_dir%/.worktrees}"
    if [ -d "$repo/.git" ] || [ -f "$repo/.git" ]; then
        git -C "$repo" worktree list --porcelain 2>/dev/null | awk -v repo="$repo" '
            /^worktree / { wt=$2 }
            /^branch / {
                br=$2; sub(/^refs\/heads\//, "", br)
                if (wt ~ /\/.worktrees\//) {
                    cmd = "stat -c %Y \"" wt "/.git\" 2>/dev/null || echo 0"
                    cmd | getline ts
                    close(cmd)
                    print wt "\t" br "\t" repo "\t" ts
                }
            }
        '
    fi
done
`
	out, err := ssh.Run(host, script)
	if err != nil {
		return nil
	}

	var entries []worktree.Entry
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 3 {
			continue
		}
		e := worktree.Entry{
			Dir:    parts[0],
			Name:   path.Base(parts[0]),
			Repo:   parts[2],
			Remote: true,
		}
		if len(parts) >= 4 {
			if ts, err := strconv.ParseInt(strings.TrimSpace(parts[3]), 10, 64); err == nil {
				e.CreatedAt = worktree.TimeUnix(ts)
			}
		}
		entries = append(entries, e)
	}
	return entries
}
