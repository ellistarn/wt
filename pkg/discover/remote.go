package discover

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/ellistarn/wt/pkg/ssh"
	"github.com/ellistarn/wt/pkg/worktree"
)

// ListRemote finds all worktrees on the remote host.
// Fans out find across top-level home directories in parallel so wall time
// scales with tree depth, not breadth — matching the local walk strategy.
// Collects worktree metadata including timestamps in a single SSH call.
func ListRemote(host string) ([]worktree.Entry, error) {
	script := `
set -eu
home=$(cd "$HOME" && pwd -P)

# Parallel find: fan out across top-level dirs, same as local worker pool.
# Writes to pipe are atomic for lines < PIPE_BUF (4096), so concurrent
# finds produce clean output.
process_repo() {
    repo="$1"
    if [ -d "$repo/.git" ] || [ -f "$repo/.git" ]; then
        git -C "$repo" worktree list --porcelain 2>/dev/null | awk -v repo="$repo" '
            /^worktree / { wt=$2 }
            /^branch / {
                br=$2; sub(/^refs\/heads\//, "", br)
                if (wt ~ /\/.worktrees\//) {
                    cmd = "stat -c %Y \"" wt "/.git\" 2>/dev/null || stat -f %m \"" wt "/.git\" 2>/dev/null || echo 0"
                    cmd | getline ts
                    close(cmd)
                    print wt "\t" br "\t" repo "\t" ts
                }
            }
        '
    fi
}

{
    # Check home root for .worktrees
    [ -d "$home/.worktrees" ] && echo "$home/.worktrees"
    # Fan out find across each top-level dir
    for d in "$home"/*/; do
        [ -d "$d" ] || continue
        name=$(basename "$d")
        case "$name" in .*) continue ;; esac
        find "$d" -maxdepth 9 -type d \( -name .worktrees -print -prune -o -name '.*' -prune \) &
    done
    wait
} | while IFS= read -r wt_dir; do
    process_repo "${wt_dir%/.worktrees}"
done
`
	out, err := ssh.Run(host, script)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to remote host %q: %w", host, err)
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
	return entries, nil
}
