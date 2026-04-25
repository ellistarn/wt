package ssh

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// TunnelPort is the local port for the SSH tunnel to the remote OpenCode server.
const TunnelPort = 4097

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

// EnsureTunnel ensures an SSH tunnel from localhost:4101 to the remote host's
// port 4100 is running. If the tunnel is already up, this is a no-op. Otherwise,
// starts ssh -fNL 4101:localhost:4100 <host> and waits for it to come up.
// The tunnel is long-lived and shared across wt invocations.
func EnsureTunnel(host string) error {
	if tunnelHealthy() {
		return nil
	}
	cmd := exec.Command("ssh", "-fNL", fmt.Sprintf("%d:localhost:4096", TunnelPort), host)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Another process may have started the tunnel between our health
		// check and this SSH invocation (race on "address already in use").
		if tunnelHealthy() {
			return nil
		}
		return fmt.Errorf("failed to start SSH tunnel to %s: %w: %s", host, err, strings.TrimSpace(string(out)))
	}
	// Wait for the tunnel to accept connections.
	for i := 0; i < 20; i++ {
		if tunnelHealthy() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("SSH tunnel started but localhost:%d not reachable", TunnelPort)
}

// tunnelHealthy checks whether the tunnel port is accepting TCP connections.
func tunnelHealthy() bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", TunnelPort), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
