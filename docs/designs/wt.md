# `wt` — Worktree Session Manager

## Problem

Multiple AI agents working on the same repository collide — they edit the same
files and corrupt each other's git state. Git worktrees solve this: each agent
gets an isolated working copy on its own branch, confined to its own directory.

The developer thinks in worktrees. "Show me everything in flight. Resume that
one. Clean up the merged ones." `wt` binds git worktrees to agent sessions
and manages the full lifecycle: create, list, resume, diff, clean up.

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

An **agent session** is implicit. The agent stores its state in a directory
inside the worktree (e.g. `.opencode/` for OpenCode, `.claude/` for Claude Code).
On resume, `wt` queries the agent for the most recent session bound to the
worktree directory and passes it explicitly (e.g., `opencode --session <id>`).
On create, the agent starts fresh in the new worktree directory.

## CLI

### `wt .` / `wt <path>` / `wt <path> <cmd>`

Create a new worktree. The first argument is a path — `.` for the current repo,
`~/src/api` for a repo elsewhere. The optional second argument specifies the
agent command to run (see [Agent Command](#agent-command)).

- Pulls the repo's current branch before creating.
- Creates a worktree at `<root>/<name>` on a new branch.
- Launches the agent in the worktree directory (child process, stdin/stdout
  attached directly).
- Pulls again on exit to refresh remote state.
- Prints a status row for the worktree on exit.

### `wt <name>`

Resume an existing worktree. `name` matches worktree names by exact match or
suffix (e.g., `a3f8c12` matches `api-a3f8c12`).

Pulls best-effort, queries the agent's session store for the most recent session
in the worktree directory, launches the agent with the session identifier (e.g.,
`opencode --session <id>`), pulls again on exit. If no previous session is found,
the agent starts fresh.

### Dispatch

The first argument is classified as:

1. **Subcommand** — `ls`, `rm`, `diff`.
2. **Path** — starts with `.`, `/`, `~/`, or contains `/`.
3. **Name** — everything else. Looked up as an existing worktree. If no
   worktree matches, error.

Bare `wt` (no arguments) prints help.

### Agent Command

The agent command is the process launched in the worktree directory. Resolution
order:

1. Explicit CLI argument: `wt <path> <cmd>` (e.g., `wt . "claude --dangerously-skip-permissions"`).
2. Stored in metadata: `<repo>/.git/worktrees/<name>/wt.json` (`cmd` field).
3. `$WT_CMD` environment variable.
4. Default: `"opencode"`.

On create, the resolved command is saved to `wt.json`. On resume, the saved
command is read from `wt.json`, falling back to `$WT_CMD` then `"opencode"`.

### Title / Activity / Token Detection

Title, activity, and token counts are obtained by querying the agent's state
store through a **provider** model. The provider is selected based on the `cmd`
field in `wt.json`:

- **OpenCode provider** — queries the OpenCode SQLite database directly
  (`$XDG_DATA_HOME/opencode/opencode.db`, `~/.local/share/opencode/opencode.db`,
  or `~/Library/Application Support/opencode/opencode.db` on macOS). A single
  query fetches the most recent non-subagent session per directory across all
  projects, returning `id`, `title`, `time_updated`, and `directory`. Token
  counts are extracted by fetching message rows for the session and parsing
  `$.tokens.total` from the JSON in Go. Base and subagent sessions (direct
  children via `parent_id`) are reported separately. All queries use basic SQL
  compatible with SQLite 3.7+ (no CTEs or `json_extract`). If `sqlite3` or the
  database is unavailable, falls back to scanning `.opencode/` directory entry
  mtimes in the worktree (no title or token data in fallback mode).
- **Claude provider** — walks the `.claude/` directory tree in the worktree and
  uses the most recent file mtime as the activity timestamp. No title or token
  data is available.

If `cmd` does not match a known provider, all providers are tried in order and
the first non-empty result wins. If no provider returns data, the title and
tokens columns show `"-"` and activity is empty.

All directory comparisons resolve symlinks via `filepath.EvalSymlinks` to produce
canonical paths. This handles systems with home directory symlinks (e.g.,
`/home/user` → `/local/home/user` on NFS-backed dev desktops) where the CLI,
database, and git may report different path representations for the same
directory.

### Session Resume

On resume, `wt` identifies the previous session to continue:

- **OpenCode** — queries the SQLite database for the most recent session whose
  `directory` matches the worktree path (both sides symlink-resolved), and passes
  `--session <id>` to the agent command. This ensures the correct session is
  resumed even when multiple worktrees share a project.
- **Claude** — no explicit binding needed; Claude Code auto-resumes by
  directory.

If no session is found, the agent starts a fresh session (correct for
first-time use or after session deletion).

Direct SQLite queries are the primary data source for both `wt ls` and resume.
The `opencode session list` CLI is not used because it is project-scoped and
only returns sessions for the project the running server is serving — worktrees
from other projects would show no session data.

### Process Detection

1. `ps -eo pid,comm` — find all PIDs with process name `opencode` or `claude`.
2. `lsof -a -p <pid> -d cwd -Fn` — resolve each PID's working directory.
3. Map: `dir → active` for each discovered working directory.

A worktree whose directory matches an active agent process is marked `active`
regardless of git state.

### `wt ls`

List all worktrees with their status. Discovers all repos under `$HOME`,
queries `git worktree list --porcelain` in each, enriches with session data,
classifies, sorts by activity (removable entries pushed to bottom).

```
WORKTREE   STATUS      TITLE                              DIR                                              TOKENS  ACTIVITY  AGE
a3f8c12    active      Rewrite Linux scheduler in Rust    ~/go/src/github.com/torvalds/linux-a3f8c12       1.2M    now       3h
b7e2a09    committed   Quantum-safe cryptography          ~/go/src/github.com/satoshi/bitcoin-b7e2a09      450K    1d        1d
e4f2a81    dirty       Finish The Winds of Winter         ~/go/src/github.com/grrm/asoiaf-e4f2a81          3.8M    5m        15y
e1d4b83    empty *     Autonomous drone delivery          ~/go/src/github.com/bezos/prime-air-e1d4b83      -       -         12y
c9a1f57    merged *    Add exceptions to Go               ~/go/src/github.com/robpike/go-c9a1f57           890K    2h        2h
d5b8e24    stale *     Actually open OpenAI               ~/go/src/github.com/altman/openai-d5b8e24        72.1M   4y        10y
7f3b1c8    empty *     Half-Life 3                        ~/go/src/github.com/gaben/hl3-7f3b1c8            -       -         18y
```

### `wt diff <name>`

Show the changes on a worktree's branch. Pulls best-effort so the diff is
computed against the latest remote state. Computes the merge-base between
`origin/<root-branch>` and HEAD, then diffs against it, capturing both committed
and uncommitted changes. Untracked files are included as new-file diffs.

Output: `--stat` summary printed directly, then the full diff piped through
`less -R` when stdout is a terminal. When piped, the full diff is printed
directly with no color and no pager.

### `wt rm [name]`

**Targeted** (`wt rm <name>`): removes the worktree unconditionally.
Force-deletes the worktree directory and branch.

**Batch** (`wt rm`): removes worktrees whose status is `merged`, `stale`, or
`empty`. These are the worktrees with no at-risk state — either the work
landed, the session went dormant with no commits, or no session was ever
created. All other statuses are kept. `wt ls` is the preview.

## Status

Status values, in priority order (highest wins). Statuses marked `*` are
removed by `wt rm`.

| Status | Meaning |
|--------|---------|
| `active` | Agent process running in this worktree |
| `dirty` | Uncommitted changes or untracked files |
| `merged *` | Changes incorporated into `origin/<root-branch>` |
| `committed` | Unique commits not yet in `origin/<root-branch>` |
| `idle` | Has session history, no unique commits |
| `stale *` | Session inactive >4 hours, no unique commits |
| `empty *` | No agent session detected (no provider returns data for this directory) |

Session state (`active`) takes priority — the worktree is in active
use. Git states (`dirty`, `merged`, `committed`) take priority next — they
describe the safety of the work. Session lifecycle states (`idle`, `stale`,
`empty`) apply when the diff is empty and the branch has no unique commits.

## Discovery

1. Walk `$HOME` up to depth 10 using a pool of 16 workers.
2. Detect `.git` directories (repos) and `.git` files (worktree checkouts).
3. Prune directories that cannot contain user repos:
   - Hidden directories (name starts with `.`).
   - Git repo roots at depth > 0 (children are source files, not repos).
   - Directories with >100 children (caches, build output).
   - `Library` at depth 1 on macOS (system directory, 28K+ subdirs).
   - Cache directories: names ending in `-cache` or `_cache`, or exactly
     `cache`, `caches`, or `__pycache__`.
   - Names containing `@` (Go module cache versioned dirs like `api@v0.28.0`).
   - `node_modules` (JS build artifacts).
4. For each discovered repo root (`.git` is a directory, not a file):
   - Fast path: skip if `.git/worktrees/` doesn't exist or is empty.
   - Run `git worktree list --porcelain`.
5. Parse porcelain output into entries. Skip the repo root itself (only
   non-root worktrees are managed).

## Merge Detection

Merge detection compares the worktree's branch against `origin/<root-branch>`.
Detection splits into two paths based on whether the branch has commits not
reachable from the target (`git rev-list --count target..branch`).

### Branch has unique commits (count > 0)

Three-phase detection, any phase returning true is sufficient:

1. **Ancestry check** — `git merge-base --is-ancestor branch target`. Catches
   fast-forward merges.
2. **Merge-tree simulation** — `git merge-tree --write-tree target branch`
   (git 2.38+). If the resulting tree equals the target tree, the branch's
   changes are already incorporated. Catches squash merges and rebases.
3. **Patch-ID comparison** — compute the patch-id of `merge-base..branch`, then
   scan patch-ids in `merge-base..target` (max 500 commits). Catches squash
   merges when merge-tree produces conflicts.

### Branch has zero unique commits (count = 0)

The branch's commits are already reachable from the target. A behind-target
check fires: if the branch tip is a proper ancestor of the target and the
worktree has a session, it is classified as merged.

## Assumptions

- Git 2.38+ is installed (for `merge-tree --write-tree`).
- The agent command is installed and on `$PATH` (default `opencode`, override
  via `$WT_CMD` or `wt <path> <cmd>`).
- The repo root checkout is clean. Worktree creation pulls the current branch.
- Local only — all worktrees are on the local filesystem.

## Scoped Out

- Auto-cleanup on branch merge (requires polling or webhook).
- Multiple discovery roots (only `$HOME` is scanned).

## Rejected Alternatives

**tmux for session persistence** — The original design. Added complexity
(session naming, client detection, `window_activity` polling, remote SSH
attach) for a problem the agent doesn't have: agents are TUIs launched
on-demand, not long-running background processes. Spawning the agent as a child
process and waiting for it to exit is simpler and sufficient.

**OpenCode server** — HTTP servers, health probes, daemon management.
Over-engineered. Agents store state in local directories per working directory.
No server needed.

**SSH transport** — User manages remote sessions separately (e.g., via tmux
control mode on the remote). This tool is local-only. Adding SSH transport
doubled the code surface for a use case better served by direct SSH + tmux on
the remote machine.

**pty for title capture** — Agents don't emit OSC title escape sequences, so
intercepting them via a pty yields nothing. Querying the agent's state store
directly is reliable and also provides activity timestamps.

**Daemon / background process** — Monitoring worktree activity, auto-cleanup,
notifications. Complexity without proportional value. The CLI is stateless; all
state lives in git and the filesystem.

**pgrep for process detection** — Platform differences, doesn't catch streaming,
requires knowing the process tree. `ps` + `lsof` resolves the actual working
directory reliably.

**Transport abstraction (interface)** — The SSH transport was removed. A single
local code path is simpler than an interface with one implementation.
