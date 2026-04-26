package git

import (
	"fmt"
	"os"
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

// DirExists checks whether a directory exists, locally or over SSH.
func DirExists(host, path string) bool {
	if host == "" {
		info, err := os.Stat(path)
		return err == nil && info.IsDir()
	}
	_, err := ssh.Run(host, fmt.Sprintf("test -d '%s'", path))
	return err == nil
}

// UpstreamRef returns the upstream tracking ref for the given branch
// (e.g., "origin/krocodile"). Returns an error if no upstream is configured.
// Works from any directory in the repo (worktree or root).
func UpstreamRef(host, dir, branch string) (string, error) {
	out, err := runGit(host, dir, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+branch)
	if err != nil || out == "" {
		return "", fmt.Errorf("no upstream configured for branch %q\n\nSet it with: git branch --set-upstream-to=origin/<base> %s", branch, branch)
	}
	return out, nil
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
