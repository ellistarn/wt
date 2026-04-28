# wt

Worktree session manager for [OpenCode](https://opencode.ai). When multiple AI
agents work on the same repo, they need isolation — separate files, separate
branches, separate git state. `wt` gives each agent its own git worktree on its
own branch, bound to a persistent OpenCode session, and manages the full
lifecycle: create, list, attach, diff, clean up. Works locally and remotely via
SSH.

```
$ wt ls
WORKTREE  STATUS     TITLE                                URI                                     TOKENS  ACTIVITY  AGE
a3f8c12   attached   Rewrite Linux scheduler in Rust      localhost:5096/~/.../torvalds/linux     380k    now       3h
b7e2a09   working    Implement quantum-safe cryptography  dev-desktop:5096/~/.../satoshi/bitcoin  240k    now       3d
4a0e9d6   dirty      Fix race in block allocator          localhost:5096/~/.../torvalds/linux     92k     5m        1d
f2c7d91   idle       Write worktree session manager       localhost:5096/~/.../ellistarn/wt       150k    5m        4d
e1d4b83   committed  Autonomous drone delivery            localhost:5096/~/.../bezos/prime-air    85k     2h        12y
c9a1f57   merged *   Add exceptions to Go                 localhost:5096/~/.../robpike/go         45k     6h        1d
d5b8e24   idle       Actually open OpenAI                 dev-desktop:5096/~/.../altman/openai    120k    10y       10y
7f3b1c8   stale *    Ship Half-Life 3                     localhost:5096/~/.../gaben/hl3          30k     18y       18y
```

Statuses, highest priority wins:

- **attached** — TUI client connected
- **working** — agent generating
- **dirty** — uncommitted changes in working tree
- **merged** \* — changes incorporated into upstream
- **committed** — unique commits not yet in upstream
- **idle** — session exists, no unique commits
- **stale** \* — session inactive >4h, no unique commits
- **empty** \* — no session was ever created

## Install

```
go install github.com/ellistarn/wt@latest  # requires Go 1.24+, Git 2.38+
```

Set `WT_REMOTE_HOST` for remote operations, `WT_OPENCODE_PORT` to override the
default port (5096).

## Commands

```
wt                        Create a new local worktree and attach
wt <name>                 Attach to an existing worktree (local or remote)
wt -r <path>              Create a new remote worktree and attach
wt ls                     List all worktrees (local and remote)
wt diff <name>            Show changes on a worktree's branch
wt rm                     Remove worktrees marked * (merged/stale/empty)
wt rm <name>              Remove a specific worktree unconditionally

Flags:
  -r, --remote              Operate on the remote dev desktop
  -h, --help                Show this help
```

## How it works

`wt` glues together Git, OpenCode, and SSH.

**Git** — Every command pulls the repo root (`git pull --ff-only --prune`).
Create adds a worktree at `<repo>/.worktrees/<name>` on a new branch with
`origin/<root-branch>` as its upstream, so worktrees always start from the
latest remote state and merge detection stays accurate against a fresh upstream.
Remove deletes the worktree directory and force-deletes the branch.

**OpenCode** — `wt` auto-starts `opencode serve` on port 5096 as a detached
process on first use, locally and on the remote host via SSH. One server per
machine, shared across all worktrees and repos. If OpenCode is already running
on a different port, set `WT_OPENCODE_PORT` to match. Sessions persist in the
OpenCode database — `wt rm` deletes the worktree and branch but never touches
session history. Reattach with `wt <name>`; the TUI reconnects and loads full
history, including work the agent completed while disconnected.

**SSH** — For remote operations, `wt` maintains a long-lived SSH tunnel (port
5097 to remote 5096) with a mux control socket for connection reuse. Health-checked
and restarted automatically. All remote git and server operations go through the
mux, amortizing SSH handshake costs.

