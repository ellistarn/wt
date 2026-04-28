package opencode

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/ellistarn/wt/pkg/cmdlog"
	"github.com/ellistarn/wt/pkg/ssh"
)

// ServerPort returns the OpenCode server port (default 5096, overridden by WT_OPENCODE_PORT).
func ServerPort() int {
	if s := os.Getenv("WT_OPENCODE_PORT"); s != "" {
		if p, err := strconv.Atoi(s); err == nil {
			return p
		}
	}
	return 5096
}

// TunnelPort returns the local tunnel endpoint port (ServerPort + 1).
func TunnelPort() int {
	return ServerPort() + 1
}

// EnsureLocalServer ensures an OpenCode server is running locally.
// If healthy, this is a no-op. Otherwise, starts opencode serve detached
// and polls until healthy.
func EnsureLocalServer() error {
	port := ServerPort()
	url := fmt.Sprintf("http://localhost:%d", port)

	if healthProbe(url) == nil {
		return nil
	}

	binary, err := exec.LookPath("opencode")
	if err != nil {
		return fmt.Errorf("opencode not found in PATH")
	}

	cmd := exec.Command(binary, "serve", "--port", strconv.Itoa(port))
	cmdlog.LogCmd(fmt.Sprintf("opencode serve --port %d", port))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err == nil {
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		defer devNull.Close()
	}

	if err := cmd.Start(); err != nil {
		// Another process may have started the server between our health
		// check and this exec (race).
		if healthProbe(url) == nil {
			return nil
		}
		return fmt.Errorf("failed to start opencode server: %w", err)
	}
	cmd.Process.Release()

	for i := 0; i < 20; i++ {
		if healthProbe(url) == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("opencode server started but not healthy at %s", url)
}

// EnsureRemoteServer ensures an OpenCode server is running on the remote host.
// Requires the SSH tunnel to be up (call ssh.EnsureTunnel first).
// Health checks go through the tunnel; if the server isn't running, starts it via SSH.
func EnsureRemoteServer(host string) error {
	tunnelURL := fmt.Sprintf("http://localhost:%d", TunnelPort())

	if healthProbe(tunnelURL) == nil {
		return nil
	}

	port := ServerPort()
	startCmd := fmt.Sprintf("$SHELL -ic 'nohup opencode serve --port %d </dev/null >/dev/null 2>&1 &'", port)
	cmdlog.LogCmd(fmt.Sprintf("%s: opencode serve --port %d", host, port))
	if _, err := ssh.Run(host, startCmd); err != nil {
		// Race — someone else may have started it.
		if healthProbe(tunnelURL) == nil {
			return nil
		}
		return fmt.Errorf("failed to start remote opencode server: %w", err)
	}

	for i := 0; i < 50; i++ {
		if healthProbe(tunnelURL) == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("remote opencode server started but not healthy through tunnel at %s", tunnelURL)
}

// healthProbe checks whether the OpenCode server is reachable with a short timeout.
func healthProbe(serverURL string) error {
	resp, err := httpGetTimeout(serverURL+"/global/health", 500*time.Millisecond)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
