package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)


// ControlPath is the shared SSH mux socket path.
const ControlPath = "/tmp/wt-ssh-%r@%h:%p"

// Run executes a command on the remote host via SSH.
func Run(host, cmd string) (string, error) {
	c := exec.Command("ssh",
		"-o", "ControlPath="+ControlPath,
		host, cmd)
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

// ResolvePath resolves a path for the remote host.
// Expands ~/... to remoteHome/..., passes absolute paths through.
func ResolvePath(path, remoteHome string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		return remoteHome + path[1:], nil
	}
	if path == "~" {
		return remoteHome, nil
	}
	if strings.HasPrefix(path, "/") {
		return path, nil
	}
	return "", fmt.Errorf("remote path must be absolute or start with ~/: %s", path)
}

