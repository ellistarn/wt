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

// Host returns WT_REMOTE_HOST or an error if unset.
func Host() (string, error) {
	host := os.Getenv("WT_REMOTE_HOST")
	if host == "" {
		return "", fmt.Errorf("WT_REMOTE_HOST is not set\n\nRemote operations require an SSH host. Set the environment variable:\n\n  export WT_REMOTE_HOST=your-dev-desktop")
	}
	return host, nil
}

// Run executes a command on the remote host via SSH, passing cmd via stdin to bash.
// Uses ControlMaster to multiplex connections — the first call to a given host
// pays the full TCP+auth cost; subsequent calls within ControlPersist reuse it.
func Run(host, cmd string) (string, error) {
	c := exec.Command("ssh",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/tmp/wt-ssh-%r@%h:%p",
		"-o", "ControlPersist=60",
		host, "bash")
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

// EnsureTunnel ensures an SSH tunnel from localhost:<localPort> to the remote
// host's <remotePort> is running. If the tunnel is already up, this is a no-op.
// Otherwise, starts ssh -fNL <localPort>:localhost:<remotePort> <host> and waits
// for it to come up. The tunnel is long-lived and shared across wt invocations.
func EnsureTunnel(host string, localPort, remotePort int) error {
	if tunnelHealthy(localPort) {
		return nil
	}
	cmd := exec.Command("ssh", "-fNL", fmt.Sprintf("%d:localhost:%d", localPort, remotePort), host)
	fmt.Fprintf(os.Stderr, "ssh -fNL %d:localhost:%d %s\n", localPort, remotePort, host)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Another process may have started the tunnel between our health
		// check and this SSH invocation (race on "address already in use").
		if tunnelHealthy(localPort) {
			return nil
		}
		return fmt.Errorf("failed to start SSH tunnel to %s: %w: %s", host, err, strings.TrimSpace(string(out)))
	}
	// Wait for the tunnel to accept connections.
	for i := 0; i < 20; i++ {
		if tunnelHealthy(localPort) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("SSH tunnel started but localhost:%d not reachable", localPort)
}

// tunnelHealthy checks whether the given port is accepting TCP connections.
func tunnelHealthy(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
