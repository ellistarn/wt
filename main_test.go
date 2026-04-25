package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestE2E_NoScreenClearOnExit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not installed")
	}

	// Build wt
	bin := filepath.Join(t.TempDir(), "wt")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s\n%s", err, out)
	}

	// Create temp git repo with an origin remote and initial commit
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "origin.git")
	repo := filepath.Join(tmp, "repo")
	for _, args := range [][]string{
		{"git", "init", "--bare", bare},
		{"git", "clone", bare, repo},
		{"git", "-C", repo, "config", "user.email", "test@test.com"},
		{"git", "-C", repo, "config", "user.name", "Test"},
		{"git", "-C", repo, "commit", "--allow-empty", "-m", "init"},
		{"git", "-C", repo, "push", "origin", "main"},
	} {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s\n%s", args, err, out)
		}
	}

	// Launch wt in a PTY so opencode gets a real terminal
	cmd := exec.Command(bin)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	defer ptmx.Close()

	// Drain PTY output in background
	var buf bytes.Buffer
	outputDone := make(chan struct{})
	go func() {
		io.Copy(&buf, ptmx)
		close(outputDone)
	}()

	// Wait for opencode to initialize, then send Ctrl+C through the PTY
	time.Sleep(3 * time.Second)
	if _, err := ptmx.Write([]byte{0x03}); err != nil {
		t.Fatalf("failed to send Ctrl+C: %v", err)
	}

	// Wait for wt to exit
	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	select {
	case <-exitCh:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("wt did not exit within 10s")
	}
	<-outputDone

	// After opencode exits its alternate screen buffer (rmcup), wt must not
	// write screen-clear escape sequences -- that destroys the user's scrollback.
	output := buf.Bytes()
	rmcup := []byte("\033[?1049l")
	if idx := bytes.LastIndex(output, rmcup); idx >= 0 {
		after := output[idx+len(rmcup):]
		if bytes.Contains(after, []byte("\033[2J")) {
			t.Errorf("screen clear escape found after alt screen exit: %q", after)
		}
	} else {
		t.Log("no alt screen transition detected; opencode may not have started fully")
	}
}
