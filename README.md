# wt

Worktree session manager. When multiple AI agents work on the same repo, they
need isolation — separate files, separate branches, separate git state. `wt`
gives each agent its own git worktree on its own branch, bound to a persistent
tmux session, and manages the full lifecycle: create, list, attach, diff, clean
up. Works locally and remotely via SSH.

```
$ wt ls
WORKTREE  STATUS     TITLE                            URI                                 ACTIVITY  AGE
a3f8c12   attached   Rewrite Linux scheduler in Rust  localhost:~/.../torvalds/linux       now       3h
b7e2a09   working    Quantum-safe cryptography        dev-desktop:~/.../satoshi/bitcoin    now       3d
e4f2a81   dirty      Finish The Winds of Winter       localhost:~/.../grrm/asoiaf          5m        15y
f2c7d91   idle       Write worktree session manager   localhost:~/.../ellistarn/wt          5m        4d
e1d4b83   committed  Autonomous drone delivery        localhost:~/.../bezos/prime-air       2h        12y
c9a1f57   merged *   Add exceptions to Go             localhost:~/.../robpike/go            6h        1d
d5b8e24   stale *    Actually open OpenAI             dev-desktop:~/.../altman/openai       10y       10y
7f3b1c8   empty *    -                                localhost:~/.../gaben/hl3             -         18y
```

Statuses, highest priority wins:

- **attached** — tmux client connected
- **working** — window received output in last 5 seconds
- **dirty** — uncommitted changes in working tree
- **merged** \* — changes incorporated into upstream
- **committed** — unique commits not yet in upstream
- **idle** — session exists, no unique commits
- **stale** \* — session inactive >4h, no unique commits
- **empty** \* — no tmux session exists

## Install

```
go install github.com/ellistarn/wt@latest
```

Requires Go 1.24+, Git 2.38+, tmux 1.9+.

Set `WT_REMOTE_HOST` for remote discovery in `wt ls`.

## Commands

```
wt .                        Create a new worktree in cwd repo, open shell
wt . <cmd>                  Create a new worktree in cwd repo, run cmd
wt <path> [cmd]             Create a new worktree in repo at path
wt <host>:<path> [cmd]      Create a new worktree on remote host
wt <host>                   Create a new worktree on remote host (~ default)
wt <name>                   Attach to an existing worktree
wt ls                       List all worktrees (local and remote)
wt diff <name>              Show changes on a worktree's branch
wt rm                       Remove worktrees marked * (merged/stale/empty)
wt rm <name>                Remove a specific worktree unconditionally

Flags:
  --root <dir>              Directory for new worktrees, relative to repo root
                              (default: .. — sibling to the repo)
                              Example: --root .worktrees
  -h, --help                Show this help
```

## How it works

`wt` glues together Git, tmux, and SSH.

**Git** — Every command pulls the repo root (`git pull --ff-only --prune`).
Create adds a worktree at `<root>/<name>` (default root `..`, placing it as a
sibling directory) on a new branch. Use `--root .worktrees` when the repo is
`$HOME` and the parent directory is not writable. Remove deletes the worktree
directory and force-deletes the branch.

**tmux** — Each worktree gets a tmux session named `wt/<name>`. The session
starts a shell in the worktree directory; any command passed to `wt` runs inside
it. Sessions persist across disconnects — detach with `Ctrl+B, d` and reattach
with `wt <name>`. Working detection uses `window_activity`; title uses
`pane_title` (processes set it via the `\033]0;title\007` escape sequence).

**SSH** — Remote paths use `host:path` syntax (e.g., `wt dev:~/src/api`). tmux
commands run over SSH with a ControlPath mux socket for connection reuse.
Attach runs `ssh -t <host> tmux attach-session -t <session>`, giving the remote
tmux session direct terminal access.

## Session cleanup

`wt rm` kills the tmux session before removing the worktree. `wt rm` (batch)
cleans up all worktrees in `merged`, `stale`, or `empty` state — including
their tmux sessions.

Orphaned tmux sessions (worktree removed outside `wt`) are cleaned up during
`wt rm` batch: any tmux session whose name matches no discovered worktree is
killed.
