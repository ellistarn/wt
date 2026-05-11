# `wt` — Worktree Session Manager

## Problem

Multiple AI agents working on the same repository collide — they edit the same
files and corrupt each other's git state. Git worktrees solve this: each agent
gets an isolated working copy on its own branch, confined to its own directory.

Agents run for minutes or hours, and the laptop closes. Local processes die. Work
must survive beyond the laptop. This requires a persistent session that outlives
the terminal window.

The developer thinks in worktrees, not tmux sessions or connection strings.
"Show me everything in flight. Attach to that one." `wt` binds git worktrees to
tmux sessions — locally and remotely — and makes creating, listing, and
resuming trivial.

## Model

A **worktree** is a git worktree at `<root>/<name>`, where `root` is a
directory relative to the repo (default `..`, placing worktrees as siblings) and
`name` equals the branch name. The worktree directory is the primary key.
Worktrees are created per unit of work and cleaned up separately when the branch
lands. The root can be overridden with `--root <dir>` (e.g. `--root .worktrees`
when the repo is `$HOME` and the parent directory is not writable).

The **root branch** is whatever branch the repo root currently has checked out
(typically `main`). Diff, status, and merge detection all compare the worktree's
branch against `origin/<root-branch>`, derived at query time from the repo root's
HEAD.

A **session** is a tmux session named `wt/<worktree-name>`. The session starts a
shell in the worktree directory. Any process can run inside it — an AI agent, an
editor, a build. `wt` does not control what runs; it only provides the shell and
the persistent environment.

A session is **working** when the tmux window has received output in the last 5
seconds, **idle** when the session exists but output has stopped, and **stale**
when the session has been idle for more than 4 hours. A worktree with no tmux
session has no status.

## Topology

tmux provides session persistence on both local and remote machines. No servers,
tunnels, or health probes.

### Local

```
laptop
  tmux new-session -d -s wt/<name>    # started by wt
       │
  wt <name> ──> tmux attach-session -t wt/<name>
```

### Remote

```
laptop                                       dev desktop
  ssh -t <host> tmux attach ──────────────>    tmux session wt/<name>
```

Sessions survive laptop close. The tmux session on the remote continues running.
On reattach, `wt <name>` SSH-attaches to the existing tmux session.

## CLI

### `wt <path> [cmd...]`

Create a new worktree. The first argument is always a path — `.` for the
current repo, `~/src/api` for a local repo elsewhere, or `host:~/src/api` for a
remote repo.

- Path only: create the worktree, open a shell in it.
- Path + args: create the worktree and run the args as a command inside the
  session.

Examples:
```
wt .                          # new worktree in cwd repo, shell
wt . claude                   # new worktree in cwd repo, run claude
wt ~/src/api opencode         # new worktree in ~/src/api, run opencode
wt dev:~/src/api              # new worktree on remote, shell
wt dev:~/src/api claude       # new worktree on remote, run claude
```

Pull the current branch before creating (hard-fail on error). Pull again on
exit so the exit summary reflects the latest remote state.

### `wt <name>`

Resume an existing worktree. `name` matches worktree names by exact match or
suffix (e.g., `a3f8c12` matches `api-a3f8c12`).

If the tmux session exists, attach to it. If it died (the worktree exists but
no tmux session), create a new session with a shell.

Pull the repo's current branch (best-effort) before attaching. Pull again on
exit.

### Dispatch

The first argument is classified as:

1. **Subcommand** — `ls`, `rm`, `diff`.
2. **Path** — starts with `.`, `/`, `~/`, or contains `:`.
3. **Name** — everything else. Looked up as an existing worktree. If no
   worktree matches, treated as a remote host with `~` as the default path.

Bare `wt` (no arguments) prints help.

### `wt rm [name]`

Remove worktrees.

**Targeted** (`wt rm <name>`): removes the worktree unconditionally.
Force-deletes the worktree directory and branch. Kills the tmux session if one
exists.

**Batch** (`wt rm`): removes worktrees whose status is `merged`, `stale`, or
`empty`. These are the worktrees with no at-risk state — either the work
landed, the session went dormant with no commits, or no session was ever
created. All other statuses are kept. `wt ls` is the preview. Additionally,
kills any `wt/` tmux sessions that don't correspond to a discovered worktree
(orphans from worktrees removed outside `wt`).

### `wt diff <name>`

Show the changes on a worktree's branch. Pulls the repo's current branch
(best-effort) so the diff is computed against the latest remote state. Computes
the merge-base between `origin/<root-branch>` and HEAD, then diffs against it,
capturing both committed and uncommitted changes. Untracked files are included
as new-file diffs.

Output: `--stat` summary printed directly (stays in scrollback), then the full
diff piped through `less -R` when stdout is a terminal. When piped, the full
diff is printed directly with no color and no pager.

### `wt ls`

List all worktrees with their status. Local worktrees (all repos under `$HOME`)
and remote worktrees (all repos on the dev desktop) are discovered concurrently
and merged into a single table sorted by most recent activity, with removable
entries (`*`) pushed to the bottom.

```
WORKTREE    STATUS       TITLE     URI                                 ACTIVITY  AGE
a3f8c12     attached     OpenCode  dev-desktop:~/.../acme/api          now       3h
b7e2a09     committed    -         localhost:~/.../acme/api             5m        1d
c9a1f57     working      Claude    dev-desktop:~/.../acme/api          now       2h
d5b8e24     merged *     -         localhost:~/.../acme/api             1h        2d
e1d4b83     empty *      -         dev-desktop:~/.../acme/web           -         2d
```

Columns:

| Column | Value |
|--------|-------|
| WORKTREE | Worktree directory name. |
| STATUS | Single highest-priority state from the table below. |
| TITLE | Terminal title set by the process running in the pane (via `\033]0;title\007` escape sequence). Any process can set this. `-` if no session. |
| URI | `host:path`. Path shortened to `~/.../parent/name` when deep. Local shows `localhost`. |
| ACTIVITY | How recently the tmux window received output. `-` if no session. |
| AGE | When the worktree was created. |

## Status

Status values, in priority order (highest wins). Statuses marked `*` are
removed by `wt rm`.

| Status | Meaning |
|--------|---------|
| `attached` | tmux client connected |
| `working` | Window received output in last 5 seconds |
| `dirty` | Visible changes in diff not accounted for by unmerged commits |
| `merged *` | Changes incorporated into `origin/<root-branch>` |
| `committed` | Unique commits not yet in `origin/<root-branch>` |
| `empty *` | No tmux session exists |
| `stale *` | Session inactive >4 hours, no unique commits |
| `idle` | Session exists, no unique commits, recent |

Session states (`attached`, `working`) take priority — the worktree is in active
use. Git states (`dirty`, `merged`, `committed`) take priority next — they
describe the safety of the work. Session lifecycle states (`empty`, `stale`,
`idle`) apply when the diff is empty and the branch has no unique commits.

### Working Detection

tmux tracks `window_activity` — the Unix timestamp of the last output received
by the window. If `now - window_activity < 5 seconds`, the session is working.
This is fully generic: any process that writes to stdout when active (streaming,
building, running tools) and goes quiet when waiting for input.

### Merge Detection

Merge detection compares the worktree's branch against `origin/<root-branch>`.
Detection splits into two paths based on whether the branch has commits not
reachable from the target (`git rev-list --count target..branch`).

When the branch has **unique commits** (count > 0): (1) ancestry check
(`merge-base --is-ancestor`); (2) merge-tree simulation (`merge-tree
--write-tree`, git 2.38+) catches squash and rebase merges; (3) patch-id
comparison catches squash merges when merge-tree produces conflicts. Phase 3
scans at most 500 commits.

When the branch has **zero unique commits** (count = 0): the branch's commits
are already reachable from the target. A behind-target check fires: if the
branch tip is a proper ancestor of the target and the worktree has a session, it
is classified as merged.

## Enrichment

For each worktree with a matching tmux session, `wt` queries:

1. `tmux list-clients -F #{client_session}` — attached status.
2. `tmux display-message -t <session> -p '#{window_activity}'` — working/idle.
3. `tmux display-message -t <session> -p '#{pane_title}'` — title.

All enrichment is best-effort. If tmux queries fail, the worktree appears
without session metadata.

## Transport

```go
type Transport interface {
    Tmux(args ...string) (string, error)
    TmuxAttach(session string) error
    Exec(cmd string) (string, error)
    Host() string
}
```

Two implementations:

- **Local**: `exec.Command("tmux", ...)` directly. `TmuxAttach` takes over the
  terminal with signal forwarding.
- **SSH**: `ssh host "cmd"` for `Tmux` and `Exec`. `TmuxAttach` runs
  `ssh -t host tmux attach-session -t <session>`.

SSH reuses a ControlPath mux socket for connection sharing.

## Configuration

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `WT_REMOTE_HOST` | For remote discovery | — | SSH hostname of the remote dev desktop |

## Assumptions

- tmux 1.9+ is installed on all machines where worktrees are managed.
- The repo root checkout is clean. Worktree creation pulls the current branch.
- The remote host is reachable via SSH.
- Processes set their terminal title if they want it to appear in `wt ls`.

## Scoped Out

- Auto-reattach on laptop wake.
- Multiple remote hosts for discovery (create supports any host via `host:path`).
- Token tracking, session store querying, agent-specific behavior.
- Process lifecycle management (restarting crashed processes).

## Rejected Alternatives

**OpenCode client/server** — HTTP servers, health probes, SSH tunnels, daemon
management. Unreliable (race conditions, orphaned tunnels, stale ports). tmux
solves session persistence as a 15-year solved problem.

**pgrep for working detection** — Checking if the pane process has children to
determine "working." Platform differences, doesn't catch streaming (no child
process), requires knowing the process tree. `window_activity` is simpler and
generic.

**Agent abstraction (pkg/agent)** — Interface with Command/ResumeCommand per
agent type. Over-engineered when `wt <path> <cmd>` lets the user pass any
command directly.

**Agent session store for enrichment** — Querying OpenCode's SQLite or Claude's
JSONL for title/tokens/activity. Fragile, agent-specific, requires parsing
internal formats. `pane_title` and `window_activity` are generic tmux features
that work with any process.

**`-c` flag for tmux new-session** — The start-directory flag was added in tmux
1.9 and some environments still run older versions. Using
`cd '<dir>' && exec $SHELL` as the session command works everywhere.

**Bash/zsh hardcoded in SSH** — `ssh host "cmd"` passes the command to the
remote user's configured login shell. No need to hardcode which shell to use.

**`-r` flag for remote** — Requires a separate env var (`WT_REMOTE_HOST`) and
different mental model. `host:path` syntax is self-contained — the target is
fully specified in one argument.
