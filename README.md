# wt

Worktree session manager for [OpenCode](https://opencode.ai).

```
$ wt ls
WORKTREE            TITLE                              STATUS    ACTIVITY  TOKENS  REPO                                    AGE
a3f8c12     Rewrite Linux scheduler in Rust     attached  now       380k    /home/torvalds/.../linux/kernel          3h
b7e2a09     Implement quantum-safe cryptography working   now       240k    [remote] /home/satoshi/.../bitcoin/src   3d
e1d4b83     Autonomous drone delivery           working   now       85k     /home/bezos/.../amazon/prime-air         12y
c9a1f57     Add exceptions to Go                idle      6h        45k     /home/robpike/.../golang/go              1d
d5b8e24     Actually open OpenAI                idle      10y       120k    /home/altman/.../openai/models           10y
f2c7d91     -                                   -         -         -       /home/user/.../acme/toolkit             1d
```

## Usage

```
wt                     # Create a new local worktree and attach
wt <name>              # Attach to an existing worktree (local or remote)
wt ls                  # List all worktrees
wt -r <path>           # Create a new remote worktree and attach
```

## Configuration

Environment variables:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `WT_REMOTE_HOST` | For `-r` operations | — | SSH hostname of the remote dev desktop |
| `WT_OPENCODE_PORT` | No | `5096` | OpenCode server port (local and remote) |
