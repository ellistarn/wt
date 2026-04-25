# wt

Worktree session manager for [OpenCode](https://opencode.ai).

```
$ wt ls
WORKTREE            TITLE                              STATUS    ACTIVITY  TOKENS  REPO                                    AGE
0424T0900-31337     Rewrite Linux scheduler in Rust     attached  now       380k    /home/torvalds/.../linux/kernel          3h
0421T1030-00001     Implement quantum-safe cryptography working   now       240k    [remote] /home/satoshi/.../bitcoin/src   3d
1213T0800-42069     Autonomous drone delivery           working   now       85k     /home/bezos/.../amazon/prime-air         12y
0423T1600-12345     Add exceptions to Go                idle      6h        45k     /home/robpike/.../golang/go              1d
1215T0900-80085     Actually open OpenAI                idle      10y       120k    /home/altman/.../openai/models           10y
0423T0900-99999     -                                   -         -         -       /home/user/.../acme/toolkit             1d
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
