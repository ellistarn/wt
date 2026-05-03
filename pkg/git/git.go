package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ellistarn/wt/pkg/cmdlog"
	"github.com/ellistarn/wt/pkg/ssh"
)

// RepoRoot returns the repo root. If host is empty, runs locally.
// For remote, pass the remote directory as extra args.
func RepoRoot(host string, dir ...string) (string, error) {
	if host == "" {
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return "", err
		}
		root := strings.TrimSpace(string(out))
		// Resolve symlinks so paths match OpenCode session directories
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		return root, nil
	}
	d := "."
	if len(dir) > 0 {
		d = dir[0]
	}
	out, err := ssh.Run(host, fmt.Sprintf("git -C '%s' rev-parse --show-toplevel", d))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// UpstreamRef returns the comparison target for worktree branches — always
// origin/<rootBranch>, where rootBranch is whatever the repo root has checked
// out (typically "main"). This is independent of any branch's git tracking
// config, which may be overwritten by git push -u to point at the branch's
// own remote ref.
func UpstreamRef(host, repo string) (string, error) {
	rootBranch, err := runGit(host, repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("cannot determine root branch in %s: %w", repo, err)
	}
	if rootBranch == "" || rootBranch == "HEAD" {
		return "", fmt.Errorf("cannot determine root branch in %s (detached HEAD?)", repo)
	}
	return "origin/" + rootBranch, nil
}

// runGit runs a git command in the given directory. If host is empty, runs locally.
func runGit(host, dir string, args ...string) (string, error) {
	if host == "" {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", "'\\''") + "'"
	}
	script := fmt.Sprintf("git -C '%s' %s", dir, strings.Join(quoted, " "))
	out, err := ssh.Run(host, script)
	return strings.TrimSpace(out), err
}

// runCapture runs a git command capturing combined stdout+stderr.
// Used for side-effect commands where output indicates what changed.
func runCapture(host, dir string, args ...string) (string, error) {
	if host == "" {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		raw, err := cmd.CombinedOutput()
		out := strings.TrimSpace(string(raw))
		if err != nil {
			return out, fmt.Errorf("%w: %s", err, out)
		}
		return out, nil
	}
	return runGit(host, dir, args...)
}

// logCmd prints a git command and its output to stderr.
func logCmd(host, dir, output string, args ...string) {
	cmd := "git -C " + dir + " " + strings.Join(args, " ")
	if host != "" {
		cmd = host + ": " + cmd
	}
	cmdlog.LogCmd(cmd)
	cmdlog.LogOutput(output)
}
