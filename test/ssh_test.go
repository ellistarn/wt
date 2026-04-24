package e2e_test

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strings"
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
	host := os.Getenv("DEV_DESKTOP_HOST")
	if host == "" {
		t.Skip("DEV_DESKTOP_HOST not set, skipping SSH tests")
	}

	name := fmt.Sprintf("wt-e2e-ssh-%d-%d", time.Now().UnixNano(), rand.Intn(100000))

	// Resolve remote home (follow symlinks for path consistency)
	remoteHome := strings.TrimSpace(sshRun(t, host, "readlink -f $HOME"))
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
	wtDir := e.repo + "/.worktrees/" + name
	sshRun(e.t, e.host, fmt.Sprintf("cd %s && git worktree add .worktrees/%s -b %s", e.repo, name, name))
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

// wt runs the local wt binary with DEV_DESKTOP_HOST set.
func (e *sshTestEnv) wt(args ...string) string {
	e.t.Helper()
	cmd := exec.Command(wtBinary, args...)
	cmd.Env = append(os.Environ(),
		"DEV_DESKTOP_HOST="+e.host,
		"XDG_DATA_HOME="+e.dataDir, // remote data dir — wt queries it over SSH
	)
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "cannot remove") {
		e.t.Logf("wt %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (e *sshTestEnv) worktreeExists(name string) bool {
	_, err := sshRunErr(e.host, fmt.Sprintf("test -d %s/.worktrees/%s", e.repo, name))
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

func TestSSH_BatchRm_DryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping SSH e2e test in short mode")
	}
	env := newSSHTestEnv(t)

	// Clean, no session → should be removed
	env.addWorktree("ssh-clean")

	// Dirty → should be skipped
	wt2 := env.addWorktree("ssh-dirty")
	sshRun(t, env.host, fmt.Sprintf("echo dirty > %s/dirty.txt", wt2))

	// Unpushed → should be skipped
	wt3 := env.addWorktree("ssh-unpushed")
	env.commitFile(wt3, "a.txt", "a", "unpushed")

	// Pushed + merged → should be removed
	wt4 := env.addWorktree("ssh-merged")
	env.commitFile(wt4, "f.txt", "f", "feature")
	env.push("ssh-merged")
	env.mergeToMain("ssh-merged")

	out := env.wt("-r", "rm", "--dry-run")
	t.Log("SSH dry-run output:\n" + out)

	assertContains(t, out, "Would remove")
	assertContains(t, out, "ssh-clean")
	assertContains(t, out, "ssh-merged")

	assertContains(t, out, "Skipped")
	assertContains(t, out, "ssh-dirty")
	assertContains(t, out, "ssh-unpushed")
}

func TestSSH_RemoteSessionQuery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping SSH e2e test in short mode")
	}
	env := newSSHTestEnv(t)

	// Create a worktree with a real opencode session on the remote
	wt := env.addWorktree("ssh-session")
	env.createSession(wt)

	// wt ls -r should show the session
	out := env.wt("-r", "ls")
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

	// wt rm should skip it (active session, default 12h stale threshold)
	out = env.wt("-r", "rm", "--dry-run")
	t.Log("SSH rm dry-run output:\n" + out)
	assertContains(t, out, "ssh-session")
	assertContains(t, out, "session active")
}
