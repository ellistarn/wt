package ssh

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ellistarn/wt/pkg/cmdlog"
)

// Host returns WT_REMOTE_HOST or an error if unset.
func Host() (string, error) {
	host := os.Getenv("WT_REMOTE_HOST")
	if host == "" {
		return "", fmt.Errorf("WT_REMOTE_HOST is not set\n\nRemote operations require an SSH host. Set the environment variable:\n\n  export WT_REMOTE_HOST=your-dev-desktop")
	}
	return host, nil
}

// controlPath is the shared SSH mux socket path. The tunnel (EnsureTunnel)
// creates the mux master; all other SSH calls (Run) reuse it.
const controlPath = "/tmp/wt-ssh-%r@%h:%p"

// Run executes a command on the remote host via SSH, passing cmd via stdin to bash.
// Reuses the tunnel's mux socket if the master is alive; otherwise connects
// directly without the mux to avoid hanging on a stale control socket.
func Run(host, cmd string) (string, error) {
	args := []string{"-o", "ConnectTimeout=10"}
	if muxMasterAlive(host) {
		args = append(args, "-o", "ControlPath="+controlPath)
	}
	args = append(args, host, "bash")
	c := exec.Command("ssh", args...)
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
//
// Before starting a new tunnel, any stale mux master and control socket are
// cleaned up. The tunnel uses SSH keepalives so dead connections are detected
// and torn down automatically rather than lingering as zombies.
func EnsureTunnel(host string, localPort, remotePort int) error {
	if tunnelHealthy(localPort) && muxMasterAlive(host) {
		return nil
	}
	// Tunnel is down or mux master is stale. Clean up before starting fresh.
	cleanupStaleTunnel(host)

	cmd := exec.Command("ssh",
		"-o", "ControlMaster=yes",
		"-o", "ControlPath="+controlPath,
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ConnectTimeout=10",
		"-fNL", fmt.Sprintf("%d:localhost:%d", localPort, remotePort), host)
	cmdlog.LogCmd(fmt.Sprintf("ssh -fNL %d:localhost:%d %s", localPort, remotePort, host))
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

// muxMasterAlive checks whether the SSH mux master process is still running
// by sending a "check" command through the control socket. Times out after
// 2 seconds to avoid hanging on a stale socket with no live master.
func muxMasterAlive(host string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx,
		"ssh",
		"-o", "ControlPath="+controlPath,
		"-O", "check", host)
	return cmd.Run() == nil
}

// cleanupStaleTunnel tears down any existing mux master for the given host
// and removes the control socket if it's stale. This prevents new tunnel
// attempts from failing with "address already in use" on the socket or
// silently routing through a dead master.
func cleanupStaleTunnel(host string) {
	// Gracefully ask the mux master to exit.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exit := exec.CommandContext(ctx,
		"ssh",
		"-o", "ControlPath="+controlPath,
		"-O", "exit", host)
	_ = exit.Run()

	// Give the master a moment to release the port and socket.
	time.Sleep(100 * time.Millisecond)
}
