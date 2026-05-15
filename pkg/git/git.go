package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ellistarn/wt/pkg/cmdlog"
)

// RepoRoot returns the repo root. Runs locally in dir.
// dir defaults to "." if not provided.
func RepoRoot(dir ...string) (string, error) {
	d := "."
	if len(dir) > 0 && dir[0] != "" {
		d = dir[0]
	}
	cmd := exec.Command("git", "-C", d, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(out))
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return root, nil
}

// UpstreamRef returns the comparison target for worktree branches — always
// origin/<rootBranch>, where rootBranch is whatever the repo root has checked
// out (typically "main").
func UpstreamRef(repo string) (string, error) {
	rootBranch, err := runGit(repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("cannot determine root branch in %s: %w", repo, err)
	}
	if rootBranch == "" || rootBranch == "HEAD" {
		return "", fmt.Errorf("cannot determine root branch in %s (detached HEAD?)", repo)
	}
	return "origin/" + rootBranch, nil
}

// runGit runs a git command in the given directory locally.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// runCapture runs a git command capturing combined stdout+stderr.
func runCapture(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	raw, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(raw))
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, out)
	}
	return out, nil
}

// logCmd prints a git command and its output to stderr.
func logCmd(dir, output string, args ...string) {
	cmd := "git -C " + dir + " " + strings.Join(args, " ")
	cmdlog.LogCmd(cmd)
	cmdlog.LogOutput(output)
}
