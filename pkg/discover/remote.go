package discover

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ellistarn/wt/pkg/ssh"
	"github.com/ellistarn/wt/pkg/worktree"
)

// ListRemote finds all worktrees on the remote host.
// Finds git repos, runs git worktree list on each, and filters for
// wt-managed worktrees in sibling layout (<repo>-<name>) or legacy
// layout (<repo>/.worktrees/<name>).
// Collects worktree metadata including timestamps in a single SSH call.
func ListRemote(host string) ([]worktree.Entry, error) {
	script := `
set -eu
home=$(cd "$HOME" && pwd -P)

# Check if a directory is a git repo and list its wt-managed worktrees.
# Matches both sibling layout (<parent>/<repobase>-<name>) and legacy
# layout (<repo>/.worktrees/<name>).
process_repo() {
    repo="$1"
    repobase=$(basename "$repo")
    repodir=$(dirname "$repo")
    if [ -d "$repo/.git" ] || [ -f "$repo/.git" ]; then
        git -C "$repo" worktree list --porcelain 2>/dev/null | awk -v repo="$repo" -v repobase="$repobase" -v repodir="$repodir" '
            /^worktree / { wt=$2 }
            /^branch / {
                br=$2; sub(/^refs\/heads\//, "", br)
                # Sibling layout: <parent>/<repobase>-<name>
                match_sibling = (wt == repodir "/" repobase "-" br)
                # COMPAT: legacy layout <repo>/.worktrees/<name>.
                # Remove once all legacy worktrees have been drained via wt rm.
                match_legacy = (wt == repo "/.worktrees/" br)
                if (match_sibling || match_legacy) {
                    cmd = "stat -c %Y \"" wt "/.git\" 2>/dev/null || stat -f %m \"" wt "/.git\" 2>/dev/null || echo 0"
                    cmd | getline ts
                    close(cmd)
                    print wt "\t" br "\t" repo "\t" ts
                }
            }
        '
    fi
}

# Find git repos by looking for .git directories.
# Fan out find across top-level dirs in parallel, same as local worker pool.
{
    # Check home root
    [ -d "$home/.git" ] && echo "$home"
    # Fan out find across each top-level dir
    for d in "$home"/*/; do
        [ -d "$d" ] || continue
        name=$(basename "$d")
        case "$name" in .*) continue ;; esac
        find "$d" -maxdepth 9 -type d \( -name .git -printf '%h\n' -prune -o -name '.*' -prune \) 2>/dev/null &
    done
    wait
} | sort -u | while IFS= read -r repo; do
    process_repo "$repo"
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
			Dir:  parts[0],
			Name: parts[1],
			Repo: parts[2],
			Host: host,
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
