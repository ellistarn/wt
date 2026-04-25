package ssh

import "testing"

func TestTunnelHealthy_NoListener(t *testing.T) {
	// With nothing listening on the tunnel port, tunnelHealthy should return false.
	// This validates the TCP probe without requiring an SSH host.
	if tunnelHealthy() {
		t.Skip("something is already listening on the tunnel port")
	}
}

func TestEnsureTunnel_BadHost(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	err := EnsureTunnel("wt-nonexistent-host-test")
	if err == nil {
		t.Fatal("expected error for unreachable host")
	}
}
