package e2e_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// sshTestEnv creates a test repo on the remote dev desktop over SSH.
// All operations run through the real SSH path.
type sshTestEnv struct {
	t       *testing.T
	host    string
	rootDir string // remote path
	repo    string // remote clone path
	dataDir string // remote XDG_DATA_HOME
}

func newSSHTestEnv(t *testing.T) *sshTestEnv {
	t.Helper()
	host := os.Getenv("WT_REMOTE_HOST")
	if host == "" {
		host = "localhost" // self-SSH: exercises the real SSH code path locally
	}

	// Probe: verify SSH to the host actually works.
	if out, err := sshRunErr(host, "echo ok"); err != nil {
		t.Skipf("SSH to %s not available (enable Remote Login for localhost): %v\n%s", host, err, out)
	}

	name := fmt.Sprintf("wt-e2e-ssh-%d-%d", time.Now().UnixNano(), rand.Intn(100000))

	// Resolve remote home (follow symlinks for path consistency)
	remoteHome := strings.TrimSpace(sshRun(t, host, `cd "$HOME" && pwd -P`))
	rootDir := remoteHome + "/" + name
	repo := rootDir + "/repo"
	dataDir := rootDir + "/data"

	sshRun(t, host, "mkdir -p "+rootDir+"/data")

	// Create bare repo and clone on remote
	sshRun(t, host, fmt.Sprintf(`
		git init --bare --initial-branch=main %s/origin.git &&
		git clone %s/origin.git %s &&
		cd %s &&
		git config user.email 'test@test.com' &&
		git config user.name 'Test' &&
		git checkout -b main &&
		git commit --allow-empty -m 'initial' &&
		git push origin main
	`, rootDir, rootDir, repo, repo))

	t.Cleanup(func() {
		sshRun(t, host, "rm -rf "+rootDir)
	})

	return &sshTestEnv{t: t, host: host, rootDir: rootDir, repo: repo, dataDir: dataDir}
}

func (e *sshTestEnv) addWorktree(name string) string {
	e.t.Helper()
	repoBase := filepath.Base(e.repo)
	repoDir := filepath.Dir(e.repo)
	wtDir := repoDir + "/" + repoBase + "-" + name
	sshRun(e.t, e.host, fmt.Sprintf(
		"cd %s && git worktree add %s -b %s && "+
			"root=$(git rev-parse --abbrev-ref HEAD) && "+
			"git branch --set-upstream-to=origin/$root %s",
		e.repo, wtDir, name, name))
	return wtDir
}

func (e *sshTestEnv) commitFile(dir, filename, content, msg string) {
	e.t.Helper()
	sshRun(e.t, e.host, fmt.Sprintf(
		"echo '%s' > %s/%s && cd %s && git add %s && git commit -m '%s'",
		content, dir, filename, dir, filename, msg))
}

func (e *sshTestEnv) push(branch string) {
	e.t.Helper()
	sshRun(e.t, e.host, fmt.Sprintf("cd %s && git push origin %s", e.repo, branch))
}

func (e *sshTestEnv) mergeToMain(branch string) {
	e.t.Helper()
	sshRun(e.t, e.host, fmt.Sprintf(
		"cd %s && git checkout main && git merge --no-ff %s -m 'merge %s' && git push origin main && git fetch origin",
		e.repo, branch, branch))
}

func (e *sshTestEnv) createSession(dir string) {
	e.t.Helper()
	// Check if opencode is available on the remote
	if _, err := sshRunErr(e.host, "which opencode"); err != nil {
		e.t.Skip("opencode not installed on remote, skipping session test")
	}
	sshRun(e.t, e.host, fmt.Sprintf(
		"XDG_DATA_HOME=%s opencode run 'respond with the single word OK' --dir %s",
		e.dataDir, dir))
}

// wt runs the local wt binary with WT_REMOTE_HOST set.
func (e *sshTestEnv) wt(args ...string) string {
	e.t.Helper()
	cmd := exec.Command(wtBinary, args...)
	cmd.Env = append(os.Environ(),
		"HOME="+e.rootDir,          // sandbox local discovery to test dir
		"WT_REMOTE_HOST="+e.host,
		"XDG_DATA_HOME="+e.dataDir, // remote data dir — wt queries it over SSH
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Logf("wt %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (e *sshTestEnv) worktreeExists(name string) bool {
	repoBase := filepath.Base(e.repo)
	repoDir := filepath.Dir(e.repo)
	_, err := sshRunErr(e.host, fmt.Sprintf("test -d %s/%s-%s", repoDir, repoBase, name))
	return err == nil
}

func sshRun(t *testing.T, host, script string) string {
	t.Helper()
	out, err := sshRunErr(host, script)
	if err != nil {
		t.Fatalf("ssh %s: %v\n%s", host, err, out)
	}
	return out
}

func sshRunErr(host, script string) (string, error) {
	cmd := exec.Command("ssh", host, "bash")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// --- SSH path tests ---

func TestSSH_Ls_UnifiedStatus(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping SSH e2e test in short mode")
	}
	env := newSSHTestEnv(t)

	// Clean, no session → empty *
	env.addWorktree("ssh-clean")

	// Dirty → dirty
	wt2 := env.addWorktree("ssh-dirty")
	sshRun(t, env.host, fmt.Sprintf("echo dirty > %s/dirty.txt", wt2))

	// Unpushed → committed
	wt3 := env.addWorktree("ssh-unpushed")
	env.commitFile(wt3, "a.txt", "a", "unpushed")

	// Pushed + merged (regular, not squash) with no session → empty *
	// After a regular merge, the branch's commits are ancestors of main,
	// so UniqueCommitCount is 0. With no session, status is "empty".
	wt4 := env.addWorktree("ssh-merged")
	env.commitFile(wt4, "f.txt", "f", "feature")
	env.push("ssh-merged")
	env.mergeToMain("ssh-merged")

	out := env.wt("ls")
	t.Log("SSH ls output:\n" + out)

	assertContains(t, out, "ssh-clean")
	assertContains(t, out, "empty *")
	assertContains(t, out, "ssh-merged")

	assertContains(t, out, "ssh-dirty")
	assertContains(t, out, "dirty")
	assertContains(t, out, "ssh-unpushed")
	assertContains(t, out, "committed")
}

func TestSSH_RemoteSessionQuery(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping SSH e2e test in short mode")
	}
	env := newSSHTestEnv(t)

	// Create a worktree with a real opencode session on the remote
	wt := env.addWorktree("ssh-session")
	env.createSession(wt)

	// wt ls should show the session
	out := env.wt("ls")
	t.Log("SSH ls output:\n" + out)

	assertContains(t, out, "ssh-session")
	// Session should show idle or working, not "-"
	if strings.Contains(out, "ssh-session") {
		lines := strings.Split(out, "\n")
		for _, line := range lines {
			if strings.Contains(line, "ssh-session") {
				if strings.Contains(line, "  -  ") || strings.HasSuffix(strings.TrimSpace(line), "-") {
					// Check it's not showing all dashes for status
					fields := strings.Fields(line)
					// STATUS is the 6th field in the table
					if len(fields) >= 6 && fields[5] == "-" {
						t.Error("session status is '-', expected 'idle' or 'working' — remote query may be broken")
					}
				}
				break
			}
		}
	}

	// wt rm should skip it (session exists, default 4h stale threshold)
	out = env.wt("rm", "--dry-run")
	t.Log("SSH rm dry-run output:\n" + out)
	assertContains(t, out, "ssh-session")
	assertContains(t, out, "keep (")
}

// TestSSH_Ls_BadHost verifies that wt ls with an unreachable WT_REMOTE_HOST
// doesn't crash — it should print a warning to stderr and exit cleanly.
// Uses a host that fails DNS resolution quickly so the test doesn't hang.
func TestSSH_Ls_BadHost(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping SSH e2e test in short mode")
	}

	// Set up a local repo so wt ls has something to discover locally.
	rootDir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(rootDir); err == nil {
		rootDir = resolved
	}
	bare := filepath.Join(rootDir, "origin.git")
	gitCmd(t, "", "init", "--bare", bare)
	repo := filepath.Join(rootDir, "repo")
	gitCmd(t, "", "clone", bare, repo)
	gitCmd(t, repo, "config", "user.email", "test@test.com")
	gitCmd(t, repo, "config", "user.name", "Test")
	gitCmd(t, repo, "commit", "--allow-empty", "-m", "initial")
	gitCmd(t, repo, "push", "origin", "main")

	cmd := exec.Command(wtBinary, "ls")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(),
		"HOME="+rootDir,
		"WT_REMOTE_HOST=does-not-exist.invalid",
	)
	out, err := cmd.CombinedOutput()
	output := string(out)
	t.Logf("wt ls (bad host) output:\n%s", output)

	// wt ls should exit 0 — remote errors are warnings, not fatal.
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Errorf("expected exit 0, got %d; output:\n%s", exitErr.ExitCode(), output)
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	// Should contain a warning about the remote being unreachable.
	if !strings.Contains(output, "warning") {
		t.Error("expected a warning about unreachable remote host in output")
	}
}

// TestCreate_Remote verifies the full remote create flow: SSH to localhost,
// create a worktree on the remote, tunnel to the mock server, and attach.
// Exercises: ssh.Host, ssh.ResolveRemoteHome, ssh.ToRemotePath, git.RepoRoot(host),
// git.WorktreeAdd(host), ssh.EnsureTunnel, opencode.EnsureRemoteServer.
func TestCreate_Remote(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping SSH e2e test in short mode")
	}

	// Force self-SSH to localhost regardless of WT_REMOTE_HOST.
	host := "localhost"
	if out, err := sshRunErr(host, "echo ok"); err != nil {
		t.Skipf("SSH to %s not available (enable Remote Login for localhost): %v\n%s", host, err, out)
	}

	// Build a minimal test repo on localhost (same machine).
	name := fmt.Sprintf("wt-e2e-create-%d-%d", time.Now().UnixNano(), rand.Intn(100000))
	remoteHome := strings.TrimSpace(sshRun(t, host, `cd "$HOME" && pwd -P`))
	rootDir := remoteHome + "/" + name
	repo := rootDir + "/repo"

	sshRun(t, host, "mkdir -p "+rootDir)
	sshRun(t, host, fmt.Sprintf(`
		git init --bare --initial-branch=main %s/origin.git &&
		git clone %s/origin.git %s &&
		cd %s &&
		git config user.email 'test@test.com' &&
		git config user.name 'Test' &&
		git checkout -b main &&
		git commit --allow-empty -m 'initial' &&
		git push origin main
	`, rootDir, rootDir, repo, repo))
	t.Cleanup(func() { sshRun(t, host, "rm -rf "+rootDir) })

	// Start a mock OpenCode server on a random port. The tunnel will forward
	// to this port, and EnsureRemoteServer health-checks through the tunnel,
	// so it short-circuits without trying to start a real opencode.
	mockPort, mockURL := startMockOpencode(t)
	_ = mockURL

	// The stub opencode must be on PATH for the attach call, which execs
	// "opencode attach ...". Since self-SSH runs on the same machine, the
	// local PATH override works for both the local and remote sides.
	stubDir := writeStubOpencode(t)
	pidFile := filepath.Join(t.TempDir(), "opencode.pid")

	// Run: wt -r <repo-path>
	// This triggers cmdRemote which:
	// 1. ssh.Host() → reads WT_REMOTE_HOST
	// 2. ssh.ResolveRemoteHome(host) → SSHes to resolve $HOME
	// 3. ssh.ToRemotePath(repoPath, remoteHome) → translates path
	// 4. git.RepoRoot(host, remotePath) → finds repo root on remote
	// 5. git.WorktreeAdd(host, ...) → creates worktree on remote
	// 6. ssh.EnsureTunnel(host, mockPort+1, mockPort) → tunnels
	// 7. opencode.EnsureRemoteServer(host) → health-checks through tunnel
	// 8. attach(...) → runs stub opencode
	cmd := exec.Command(wtBinary, "-r", repo)
	cmd.Env = append(os.Environ(),
		"HOME="+remoteHome,
		"WT_REMOTE_HOST="+host,
		"WT_OPENCODE_PORT="+strconv.Itoa(mockPort),
		"OPENCODE_PIDFILE="+pidFile,
		"PATH="+stubDir+":"+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	output := string(out)
	t.Logf("wt -r output:\n%s", output)

	t.Cleanup(func() {
		data, readErr := os.ReadFile(pidFile)
		if readErr != nil {
			return
		}
		pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if convErr != nil {
			return
		}
		syscall.Kill(pid, syscall.SIGTERM)
	})

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("expected exit 0, got %d; output:\n%s", exitErr.ExitCode(), output)
		} else {
			t.Fatalf("unexpected error: %v\n%s", err, output)
		}
	}

	// Output should contain a worktree name matching <repobase>-<7hex>
	repoBase := filepath.Base(repo)
	nameRe := regexp.MustCompile(regexp.QuoteMeta(repoBase) + `-[0-9a-f]{7}`)
	match := nameRe.FindString(output)
	if match == "" {
		t.Fatalf("output does not contain a worktree name matching %s-<7hex>:\n%s", repoBase, output)
	}

	// Verify the worktree directory was created on the remote
	repoDir := filepath.Dir(repo)
	wtDir := repoDir + "/" + match
	if _, sshErr := sshRunErr(host, fmt.Sprintf("test -d '%s'", wtDir)); sshErr != nil {
		t.Errorf("worktree directory %s was not created on remote", wtDir)
	}
}

// startMockOpencode starts a minimal mock OpenCode server that responds to
// /global/health, /session, and /session/<id>/message. Returns the port and URL.
func startMockOpencode(t *testing.T) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/global/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"healthy": true})
	})
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/session/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]any{})
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	url := "http://" + ln.Addr().String()
	return port, url
}
