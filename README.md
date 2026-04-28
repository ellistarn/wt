# wt

Worktree session manager for [OpenCode](https://opencode.ai). When multiple AI
agents work on the same repo, they need isolation — separate files, separate
branches, separate git state. `wt` gives each agent its own git worktree on its
own branch, bound to a persistent OpenCode session, and manages the full
lifecycle: create, list, attach, diff, clean up. Works locally and remotely via
SSH.

```
$ wt ls
WORKTREE       STATUS       TITLE                                URI                                           TOKENS  ACTIVITY  AGE
a3f8c12  attached     Rewrite Linux scheduler in Rust      localhost:5096/~/.../linux/kernel            380k    now       3h
b7e2a09  working      Implement quantum-safe cryptography  dev-desktop:5096/~/.../bitcoin/src          240k    now       3d
e1d4b83  committed    Autonomous drone delivery            localhost:5096/~/.../amazon/prime-air       85k     2h        12d
4a0e9d6  dirty        Fix race in block allocator          localhost:5096/~/.../linux/mm               92k     5m        1d
c9a1f57  merged *     Add exceptions to Go                 localhost:5096/~/.../golang/go              45k     6h        1d
d5b8e24  idle         Actually open OpenAI                 dev-desktop:5096/~/.../openai/models        120k    3h        10d
7f3b1c8  stale *      Ship Half-Life 3                     localhost:5096/~/.../valve/hl3              30k     2d        5d
f2c7d91  idle         Write worktree session manager       localhost:5096/~/.../ellistarn/wt           150k    5m        4d
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

## Worktree lifecycle

Each worktree branches from whatever the repo root has checked out (typically
main), with `origin/<root-branch>` as its merge target. `wt` pulls the repo
before creating a worktree and again after you exit, so worktrees start from the
latest remote state and merge detection stays accurate against a fresh upstream.

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

