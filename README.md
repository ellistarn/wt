# wt

Worktree session manager for [OpenCode](https://opencode.ai). When multiple AI
agents work on the same repo, they need isolation — separate files, separate
branches, separate git state. `wt` gives each agent its own git worktree on its
own branch, bound to a persistent OpenCode session, and manages the full
lifecycle: create, list, attach, diff, clean up. Works locally and remotely via
SSH.

```
$ wt ls
WORKTREE       TITLE                                STATUS       ACTIVITY  TOKENS  REPO                                      AGE
a3f8c12  Rewrite Linux scheduler in Rust      attached     now       380k    /home/torvalds/.../linux/kernel            3h
b7e2a09  Implement quantum-safe cryptography  working      now       240k    [remote] /home/satoshi/.../bitcoin/src     3d
e1d4b83  Autonomous drone delivery            committed    2h        85k     /home/bezos/.../amazon/prime-air           12y
4a0e9d6  Fix race in block allocator          dirty        5m        92k     /home/torvalds/.../linux/mm                1d
c9a1f57  Add exceptions to Go                 merged *     6h        45k     /home/robpike/.../golang/go                1d
d5b8e24  Actually open OpenAI                 idle         10y       120k    /home/altman/.../openai/models             10y
7f3b1c8  Finish TAOCP volume 5                stale *      2d        30k     /home/knuth/.../taocp/vol5                50y
f2c7d91  Write worktree session manager       idle         5m        150k    /home/ellistarn/.../ellistarn/wt           4d
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

`wt ls` is the preview for `wt rm`. Merge detection handles regular merges,
squash merges, and rebase merges against `origin/<root-branch>`.
