# `wt` — Worktree Session Manager

## Problem

Multiple AI agents working on the same repository collide -- they edit the same
files and corrupt each other's git state. Git worktrees solve this: each agent
gets an isolated working copy on its own branch, confined to its own directory.
OpenCode enforces the boundary -- given a directory, it stays in it.

Agents run for minutes or hours, and the laptop closes. Local processes die. Work
must survive beyond the laptop. This requires a persistent environment: OpenCode
servers that run continuously, with sessions that outlive any client connection.

The developer thinks in worktrees, not sessions, ports, or connection strings.
"Show me everything in flight. Attach to that one." `wt` binds git worktrees to
OpenCode sessions -- locally and remotely -- and makes creating, listing, and
resuming trivial.

## Model

A **worktree** is a git worktree at `<repo>/.worktrees/<name>`, where `name`
equals the branch name. The worktree directory is the primary key. Worktrees are
created per unit of work and cleaned up separately when the branch lands.

A **session** is an OpenCode conversation bound to a worktree directory. Sessions
persist on the server and carry a title (auto-generated from the first prompt)
and full message history. A worktree can have multiple sessions; the most recent
is attached by default. Session lifecycle (creating new sessions, forking) is
managed from within OpenCode, not by this tool.

A session is **working** when the agent is actively generating a response, and
**idle** otherwise. A worktree with no session has no status.

An **OpenCode server** is a persistent process running `opencode serve`. One
server hosts all worktrees across all repos. Every API endpoint accepts a
`directory` parameter to scope requests to a specific worktree. TUI clients are
stateless -- they re-fetch everything on connect.

Multiple TUI clients can attach to the same server and session simultaneously.
OpenCode synchronizes state across clients via server-sent events.

## Topology

Local and remote worktrees use the same architecture: a persistent OpenCode
server with TUI clients attached via `opencode attach`.

### Local

The OpenCode server runs on the laptop as a daemon (e.g. launchd) at a known
port (`localhost:9000`). Worktrees and sessions live on the laptop.

```
laptop
  opencode serve --port 9000         # daemon, always running
       │
  wt <name> ──> opencode attach http://localhost:9000
                  --dir <repo>/.worktrees/<name>
                  --session <id>
```

### Remote

The OpenCode server runs on the dev desktop. TUI clients connect through an SSH
tunnel.

```
laptop                                  dev desktop
  wt -r ~/src/acme/api ──SSH──>            git worktree add ...
                                                │
  opencode attach ────tunnel────────> opencode serve
    --dir <remote worktree path>
    --session <id>
```

Sessions survive laptop close. On reattach, the TUI reconnects and loads the
full session state, including any work the agent completed while disconnected.

## CLI

### `wt [name]`

Create or resume a worktree.

- No args: pull the current branch from origin to ensure the worktree starts
  from the latest remote state. Create a new worktree. Attach.
- With `name`: resume `<repo>/.worktrees/<name>`. Attach.

### `wt -r <path>`

Create a new remote worktree.

- `path` identifies the repo as a local-style path
  (e.g., `~/src/acme/api`), translated to the remote
  equivalent.
- Pulls the current branch from origin, then creates a new worktree. Attach.

### Attach

All attach operations follow the same steps:

1. Resolve the server URL (local: `http://localhost:9000`, remote: tunnel URL).
2. Health check: `GET /global/health`. Fail with a clear message if the server
   is not running.
3. Query `GET /session` filtered by the worktree directory. Select the most
   recently updated session.
4. Run `opencode attach <server> --dir <dir> --session <id>`.

If no session exists for the worktree, `opencode attach` is run without
`--session`. OpenCode creates a new session on first prompt.

### `wt rm [name] [--force] [--stale N] [--dry-run]`

Remove worktrees that are safe to clean up.

A worktree is safe to remove when there is nothing left to lose and no reason
to come back.

**Nothing left to lose** — working tree is clean, no unpushed commits, and
agent is not actively generating.

**No reason to come back** — branch is merged, no session exists, or session
is stale (inactive longer than `--stale` threshold, default 12 hours) with no
commits on the branch. Squash merges cannot be safely detected because the
commit hash has changed. These worktrees are removed when stale or by targeted
`wt rm <name>`.

- No args: batch mode. Requires both. Print what was removed and skipped.
- With `name`: targeted mode. Requires nothing left to lose; warns about
  reason to come back.
- `--stale N`: override the stale threshold (default 12 hours).
- `--dry-run`: preview without removing.
- `--force`: skip all checks.

Fetches from origin before checking. Removal deletes the worktree directory
and the branch. Session history in the database is not touched.

### `wt ls`

List all worktrees and their session status. Local worktrees (all repos under
`$HOME`) and remote worktrees (all repos on the dev desktop) are discovered
concurrently and merged into a single table sorted by most recent activity.

Session metadata is fetched from the OpenCode server API, not from the database
directly. For each server (local and remote):

1. `GET /session?directory=<dir>` — per worktree, returns sessions across
   projects. The most recently updated session is selected.
2. `GET /session/<id>/message` — per session, reads the last assistant
   message's total token count (context window size) and derives working/idle
   from whether that message has completed.

```
WORKTREE            TITLE                           STATUS    ACTIVITY  TOKENS  REPO                              AGE
0423T1430-12847     Fix auth handler validation      attached  now       150k    [remote] /home/user/.../acme/api   3h
0423T1600-4419      Refactor config parser           idle      5m        42k     /Users/user/.../acme/api           1d
0423T1700-8812      Migrate database schema          working   now       12k     [remote] /home/user/.../acme/api   2h
0421T1100-5531      -                                -         -         -       [remote] /home/user/.../acme/web   2d
```

Columns:

| Column | Value |
|--------|-------|
| WORKTREE | Worktree directory name (the stable identifier even if the branch is renamed). |
| TITLE | Session title, auto-generated from the first prompt. |
| STATUS | Highest-priority state: `attached` (TUI client connected), `working` (agent generating, no client), `idle` (session exists, no activity). Attachment is detected by scanning local `opencode attach` processes. No session shows `-`. |
| ACTIVITY | How recently the session was active. `now` when the agent is streaming. When idle, shows when the last assistant message completed (e.g. `5m`, `3h`, `1d`). |
| TOKENS | Context window size from the last assistant message in the most recent session. Formatted as `12k`, `150k`. |
| REPO | Repo root, shortened to `<home>/.../parent/name`. `[remote]` prefix for remote worktrees. |
| AGE | When the worktree was created. |

`-` in any column means the value is unavailable. A worktree with no session
shows `-` for STATUS, ACTIVITY, TOKENS, and TITLE. Attaching to such a worktree
creates a session on first prompt; subsequent listings show its status and title.

## Reconnection

1. Laptop opens. SSH tunnel restarts.
2. `wt ls` shows everything in flight.
3. `wt 0423T1430-12847` resumes (works for both local and remote worktrees).

## Assumptions

- The repo root checkout is on the default branch and clean. Worktree creation
  pulls this branch, so conflicts or uncommitted changes would cause a failure.
- An SSH tunnel to the dev desktop is established and maintained externally.
- The OpenCode server runs persistently on both the laptop (daemon) and the dev
  desktop, managed externally.

## Implementation

Go binary. Shells out to `ssh` for remote git operations and to `opencode` for
TUI attachment. Queries the OpenCode HTTP API for session metadata in listings
and for session discovery when attaching.

## Scoped Out

- OpenCode server lifecycle (daemon setup, launchd plist).
- SSH tunnel management.
- Auto-reattach on laptop wake.
- Session lifecycle (new, fork). Managed from within OpenCode.
- Multiple remote hosts.
- Conflict detection when running bare `opencode` alongside the daemon.

## Rejected Alternatives

**Bash** — JSON parsing for the OpenCode API requires external tooling. Go gives a
static binary with native HTTP and JSON support.

**cmux SSH for remote TUI** — The TUI runs on the dev desktop, viewed through a
forwarded terminal. Breaks on disconnect. `opencode attach` runs the TUI locally
and reconnects cleanly.

**Server per worktree** — Port management overhead. The OpenCode server's
`directory` parameter handles multi-project in one process.

**Separate local/remote tools** — Same workflow, different transport. One tool with
a `-r` flag.

**Embedded server for local** — Running bare `opencode` starts a new server per
invocation. Double-attaching creates a second server with an empty session instead
of joining the existing one. A persistent server with `opencode attach` gives
consistent multi-client behavior.

**SQLite for session metadata** — Querying the OpenCode database directly is a
layer violation. The HTTP API is the stable contract.
