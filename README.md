# wt

Worktree manager for AI coding agents. Each agent gets its own git worktree on
its own branch. `wt` manages the lifecycle: create, list, resume, diff, clean up.

```
$ wt ls
WORKTREE   STATUS      TITLE                              DIR                                              ACTIVITY  AGE
a3f8c12    active      Rewrite Linux scheduler in Rust    ~/go/src/github.com/torvalds/linux-a3f8c12       now       3h
b7e2a09    committed   Quantum-safe cryptography          ~/go/src/github.com/satoshi/bitcoin-b7e2a09      1d        1d
e4f2a81    dirty       Finish The Winds of Winter         ~/go/src/github.com/grrm/asoiaf-e4f2a81          5m        15y
e1d4b83    empty *     Autonomous drone delivery          ~/go/src/github.com/bezos/prime-air-e1d4b83      -         12y
c9a1f57    merged *    Add exceptions to Go               ~/go/src/github.com/robpike/go-c9a1f57           2h        2h
d5b8e24    stale *     Actually open OpenAI               ~/go/src/github.com/altman/openai-d5b8e24        4y        10y
7f3b1c8    empty *     Half-Life 3                        ~/go/src/github.com/gaben/hl3-7f3b1c8            -         18y
```

## Status

Highest priority wins:

- **active** — agent process running in this worktree
- **dirty** — uncommitted changes or untracked files
- **merged** \* — changes incorporated into upstream
- **committed** — unique commits not yet in upstream
- **idle** — has session history, no unique commits
- **stale** \* — session inactive >4h, no unique commits
- **empty** \* — no agent session detected

Statuses marked `*` are removed by `wt rm`.

## Install

```
go install github.com/ellistarn/wt@latest
```

Requires Go 1.24+, Git 2.38+, and an agent (`opencode`, `claude`, or any command).

## Commands

```
wt .                        Create worktree in cwd repo, run agent
wt <path>                   Create worktree in repo at path, run agent
wt <path> <cmd>             Create worktree, run specified command
wt <name>                   Resume existing worktree
wt ls                       List all worktrees with status
wt diff <name>              Show changes on a worktree's branch
wt rm                       Remove worktrees marked * (merged/stale/empty)
wt rm <name>                Remove a specific worktree unconditionally

Flags:
  --root <dir>              Directory for new worktrees, relative to repo root
                              (default: .. — sibling to the repo)
  -h, --help                Show this help
```

## How it works

**Git** — Pulls the repo before creating/resuming. Creates a worktree at
`<root>/<name>` on a new branch (`<project>-<hex>`). Remove deletes the
worktree directory and force-deletes the branch.

**Agent** — Spawns the agent command as a child process in the worktree
directory (stdin/stdout attached). The agent command is resolved as:
explicit CLI arg → `wt.json` metadata → `$WT_CMD` → `"opencode"`.

**Session detection** — Queries agent state stores for title and activity.
OpenCode: `sqlite3 ~/.local/share/opencode/opencode.db` (title + timestamp).
Claude: `.claude/` directory mtime in the worktree. Process detection via
`ps -eo pid,comm` for agent processes + `lsof` for their working directories.

**Discovery** — Parallel walk of `$HOME` (depth 10, 16 workers). Only spawns
`git worktree list` for repos with a non-empty `.git/worktrees/` directory.

## Configuration

| Setting | Description |
|---------|-------------|
| `$WT_CMD` | Default agent command (default: `opencode`) |
| `--root <dir>` | Worktree placement relative to repo (default: `..`) |
| `wt.json` | Per-worktree metadata at `<repo>/.git/worktrees/<name>/wt.json` |
