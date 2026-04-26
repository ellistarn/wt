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

A session is **working** when the agent is actively generating a response,
**idle** when the session exists but is not generating, and **stale** when the
session has been idle for more than 12 hours. A worktree with no session has no
status.

An **OpenCode server** is a persistent process running `opencode serve`. One
server per machine hosts all worktrees across all repos. The bulk session API
(`GET /session`) is scoped to the active project, but directory-filtered queries
(`GET /session?directory=<dir>`) cross project boundaries. TUI clients are
stateless -- they re-fetch everything on connect.

Multiple TUI clients can attach to the same server and session simultaneously.
OpenCode synchronizes state across clients via server-sent events.

## Topology

Local and remote worktrees use the same architecture: a persistent OpenCode
server with TUI clients attached via `opencode attach`.

### Local

The OpenCode server runs on the laptop on port 5096. `wt` ensures the server is
running before any operation — if no server is healthy, `wt` starts
`opencode serve --port 5096` as a detached process. The server outlives the `wt`
invocation and is reused across commands.

```
laptop
  opencode serve                     # auto-started by wt, port 5096
       │
  wt <name> ──> opencode attach http://localhost:5096
                  --dir <repo>/.worktrees/<name>
                  --session <id>
```

### Remote

Both machines run an OpenCode server on port 5096, each auto-started by `wt` on
first use. `wt` maintains an SSH tunnel on port 5097 forwarding to the remote's
5096. TUI clients run locally, attaching through the tunnel.

```
laptop                                       dev desktop
  opencode serve                               opencode serve
  (port 5096)                                  (port 5096)
                                                        ▲
  ssh -fNL 5097:localhost:5096 ─────────────────────────┘
       (tunnel, long-lived)

  wt <name> ──> opencode attach http://localhost:5097
                  --dir <remote worktree path>
                  --session <id>
```

Sessions survive laptop close. On reattach, the TUI reconnects and loads the
full session state, including any work the agent completed while disconnected.

### Tunnel

`wt` ensures the SSH tunnel exists before any remote HTTP operation. Health
check: TCP connect to `localhost:5097`. If down, start
`ssh -fNL 5097:localhost:5096 <host>`. The tunnel is long-lived (`ssh -f`
backgrounds the process), shared across `wt` invocations. If the tunnel dies,
the next invocation restarts it.

### Server Lifecycle

`wt` ensures an OpenCode server is running before any operation that needs one,
using the same pattern as the tunnel: health check, start if not healthy, reuse
across invocations.

Locally, health check `GET /global/health` on `localhost:5096`. If down, start
`opencode serve --port 5096` as a detached process. Remotely, the same health
check goes through the tunnel (`localhost:5097`). If down, start the server via
`ssh <host>`. Both cases poll until healthy or error.

The server outlives the `wt` invocation. Multiple `wt` invocations reuse the
same server without starting duplicates. If someone manually starts
`opencode serve --port 5096`, `wt` reuses it. The server dies on reboot or
crash; the next `wt` invocation restarts it.

Both `wt`-managed and user-started servers share the same data directory
(~/.opencode or configured). Sessions are stored globally in SQLite, but the
bulk session API filters by the active project. Directory-filtered queries
cross project boundaries, which is how `wt ls` retrieves sessions for
worktrees across all repos.

If the server dies mid-agent, the next `wt` command restarts it. OpenCode
sessions are crash-tolerant — incomplete messages are treated as interrupted
history, and reattaching resumes the session.

## CLI

### `wt [name]`

Create or resume a worktree.

- No args: pull the current branch to ensure the worktree starts from the
  latest remote state. Create a new worktree. Attach.
- With `name`: pull the repo's default branch (best-effort) to keep it fresh
  for future worktree creation and merge detection. Resume
  `<repo>/.worktrees/<name>`. Attach.

### `wt -r <path>`

Create a new remote worktree.

- `path` identifies the repo as a local-style path
  (e.g., `~/src/acme/api`), translated to the remote
  equivalent.
- Pulls the current branch, then creates a new worktree. Attach.

### Attach

All attach operations follow the same steps:

1. Resolve the server URL (local: `http://localhost:5096`, remote:
   `http://localhost:5097` via the SSH tunnel).
2. Ensure the server is running (local or tunnel + remote). Fail with a clear
   message if the server cannot be started.
3. Query `GET /session` filtered by the worktree directory. Select the most
   recently updated session.
4. Run `opencode attach <server> --dir <dir> --session <id>`.

If no session exists for the worktree, `opencode attach` is run without
`--session`. OpenCode creates a new session on first prompt.

### `wt rm [name]`

Remove worktrees.

**Targeted** (`wt rm <name>`): removes the worktree unconditionally.
Force-deletes the worktree directory and branch. The user already knows the
state from `wt ls`.

**Batch** (`wt rm`): removes worktrees whose status is `merged`, `stale`, or
`empty`. These are the worktrees with no at-risk state — either the work
landed, the session went dormant with no commits, or no session was ever
created. All other statuses are kept. `wt ls` is the preview.

Merge detection is three-phase: (1) ancestry check (`merge-base --is-ancestor`)
catches regular and fast-forward merges; (2) merge-tree simulation
(`merge-tree --write-tree`, requires git 2.38+) catches squash merges when the
branch merges cleanly; (3) patch-id comparison catches squash merges when
merge-tree produces conflicts (e.g., main has moved forward and later commits
touch the same files). Phase 3 computes the branch's aggregate diff patch-id
and searches `origin/<default>` for a commit with a matching patch-id. This
works for both single-commit and multi-commit squash merges.

All read paths (ls, rm) fetch from origin to ensure remote-tracking refs are
current. Removal deletes the worktree
directory and the branch. Session history in the database is not touched.

### `wt ls`

List all worktrees with their status. Local worktrees (all repos under `$HOME`)
and remote worktrees (all repos on the dev desktop) are discovered concurrently
and merged into a single table sorted by most recent activity.

Session metadata is fetched from the OpenCode server API, not from the database
directly. For each server (local and remote), per worktree (parallel, bounded to
8 concurrent):

1. `GET /session?directory=<dir>` — fetches the most recent session for that
   worktree. Per-directory queries cross OpenCode project boundaries, unlike the
   bulk `GET /session` endpoint which is scoped to the active project.
2. `GET /session/<id>/message` — reads the last assistant message's total token
   count (context window size) and derives working/idle from whether that message
   has completed.

Working/idle detection uses the last assistant message's `completed` timestamp:
- `completed == null` → working (streaming)
- `completed != null` → idle

The server's `UpdatedAt` does not advance during streaming, so there is no
reliable way to distinguish a long-running response from a crash orphan.
`completed == null` is treated as working — a false positive ("working" on a
crashed session) is preferable to a false negative (hiding active work).

Git state is determined per worktree (parallel, bounded to 8 concurrent) by
checking the working tree and branch against `origin/<default>`.

```
WORKTREE            STATUS       TITLE                           REPO                              TOKENS  ACTIVITY  AGE
0423T1430-12847     attached     Fix auth handler validation      [remote] /home/user/.../acme/api   150k    now       3h
0423T1600-4419      committed    Refactor config parser           /Users/user/.../acme/api           42k     5m        1d
0423T1700-8812      working      Migrate database schema          [remote] /home/user/.../acme/api   12k     now       2h
0423T0900-2210      merged *     Add retry logic                  /Users/user/.../acme/api           80k     1h        2d
0421T1100-5531      empty *      -                                [remote] /home/user/.../acme/web   -       -         2d
```

Columns:

| Column | Value |
|--------|-------|
| WORKTREE | Worktree directory name (the stable identifier even if the branch is renamed). |
| STATUS | Single highest-priority state from the table below. |
| TITLE | Session title, auto-generated from the first prompt. `-` if no session. |
| ACTIVITY | How recently the session was active. `now` when the agent is streaming. When idle, shows when the last assistant message completed (e.g. `5m`, `3h`, `1d`). `-` if no session. |
| TOKENS | Context window size from the last assistant message in the most recent session. Formatted as `12k`, `150k`. `-` if no session. |
| REPO | Repo root, shortened to `<home>/.../parent/name`. `[remote]` prefix for remote worktrees. |
| AGE | When the worktree was created. |

Status values, in priority order (highest wins). Statuses marked `*` are
removed by `wt rm`.

| Status | Meaning |
|--------|---------|
| `attached` | TUI client connected |
| `working` | Agent streaming (last assistant message incomplete) |
| `dirty` | Uncommitted changes in working tree |
| `merged *` | Changes incorporated into `origin/<default>` |
| `committed` | Unique commits not in `origin/<default>` |
| `idle` | Session exists, no unique commits, recent |
| `stale *` | Session inactive >12 hours, no unique commits |
| `empty *` | No session was ever created |

Session states (`attached`, `working`) take priority — the worktree is in active
use. Git states (`dirty`, `merged`, `committed`) take priority next — they
describe the safety of the work. Session lifecycle states (`idle`, `stale`,
`empty`) apply when the working tree is clean and the branch has no unique
commits. Attachment is detected by scanning local `opencode attach` processes.

## Reconnection

1. Laptop opens.
2. `wt ls` shows everything in flight (ensures the server and tunnel as needed).
3. `wt 0423T1430-12847` resumes (works for both local and remote worktrees).

## Assumptions

- The repo root checkout is on the default branch and clean. Worktree creation
  pulls this branch, so conflicts or uncommitted changes would cause a failure.
- The remote host is reachable via SSH.
- `opencode` is available on PATH (locally and on the remote host).

## Configuration

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `WT_REMOTE_HOST` | For remote operations | — | SSH hostname of the remote dev desktop |
| `WT_OPENCODE_PORT` | No | `5096` | OpenCode server port (local and remote). Tunnel port is always server port + 1. |

## Implementation

Go binary. Shells out to `ssh` for remote git operations and to `opencode` for
TUI attachment. Queries the OpenCode HTTP API for session metadata in listings
and for session discovery when attaching.

## Scoped Out

- Auto-reattach on laptop wake.
- Session lifecycle (new, fork). Managed from within OpenCode.
- Multiple remote hosts.
- Conflict detection when running bare `opencode` alongside the managed server.

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

**External server management** — Running `opencode serve` as a systemd unit or
launchd plist requires manual setup on every machine and creates env-wiring
problems (systemd does not inherit the user's shell environment — AWS
credentials, PATH). `wt` starting the server as a detached child inherits the
user's env and makes the tool self-contained.

**External SSH tunnel** — Couples `wt` to external infrastructure (launchd plist,
shell aliases). The tunnel is a prerequisite for every remote operation; managing
it internally makes the tool self-contained.

**Per-command SSH tunnel** — SSH handshake costs ~500ms per invocation. A
long-lived tunnel amortizes the cost across all `wt` commands.

**Remote TUI over SSH** — Running the TUI on the remote host and forwarding the
terminal adds latency to every keystroke and breaks the local-client model where
`opencode attach` runs on the laptop.

**`wt rm --dry-run`** — `wt ls` already shows the unified status that determines
what `wt rm` will remove. A separate preview command duplicates information the
user can read from `ls`.
