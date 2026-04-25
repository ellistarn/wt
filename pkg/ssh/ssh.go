package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Host returns WT_REMOTE_HOST or an error if unset.
func Host() (string, error) {
	host := os.Getenv("WT_REMOTE_HOST")
	if host == "" {
		return "", fmt.Errorf("WT_REMOTE_HOST is not set\n\nRemote operations require an SSH host. Set the environment variable:\n\n  export WT_REMOTE_HOST=your-dev-desktop")
	}
	return host, nil
}

// Run executes a command on the remote host via SSH, passing cmd via stdin to bash.
func Run(host, cmd string) (string, error) {
	c := exec.Command("ssh", host, "bash")
	c.Stdin = strings.NewReader(cmd)
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("ssh %s: %w: %s", host, err, string(out))
	}
	return string(out), nil
}

// ResolveRemoteHome resolves and caches the remote physical home directory.
func ResolveRemoteHome(host string) (string, error) {
	cacheDir, _ := os.UserCacheDir()
	cachePath := filepath.Join(cacheDir, "wt-remote-home-"+host)

	if data, err := os.ReadFile(cachePath); err == nil {
		return strings.TrimSpace(string(data)), nil
	}

	out, err := Run(host, `cd "$HOME" && pwd -P`)
	if err != nil {
		return "", fmt.Errorf("cannot resolve remote HOME: %w", err)
	}
	home := strings.TrimSpace(out)

	_ = os.MkdirAll(filepath.Dir(cachePath), 0755)
	_ = os.WriteFile(cachePath, []byte(home), 0644)
	return home, nil
}

// ToRemotePath translates a local path to its remote equivalent.
func ToRemotePath(localPath, remoteHome string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine HOME: %w", err)
	}

	// Expand ~ explicitly
	if strings.HasPrefix(localPath, "~/") {
		localPath = home + localPath[1:]
	}

	if !strings.HasPrefix(localPath, home) {
		return "", fmt.Errorf("path must start with $HOME: %s", localPath)
	}

	return remoteHome + localPath[len(home):], nil
}
