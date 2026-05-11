package e2e_test

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// sshTestEnv creates a test repo on the remote dev desktop over SSH.
type sshTestEnv struct {
	t       *testing.T
	host    string
	rootDir string
	repo    string
	stubDir string
}

func newSSHTestEnv(t *testing.T) *sshTestEnv {
	t.Helper()
	host := "localhost"

	if out, err := sshRunErr(host, "echo ok"); err != nil {
		t.Skipf("SSH to %s not available (enable Remote Login for localhost): %v\n%s", host, err, out)
	}

	name := fmt.Sprintf("wt-e2e-ssh-%d-%d", time.Now().UnixNano(), rand.Intn(100000))

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

	t.Cleanup(func() {
		sshRun(t, host, "rm -rf "+rootDir)
	})

	// Write stub tmux on the remote
	sshRun(t, host, fmt.Sprintf(`
mkdir -p %s/stubs
cat > %s/stubs/tmux << 'STUBEOF'
#!/bin/sh
SESSIONS_FILE="$WT_TEST_SESSIONS"
case "$1" in
    has-session) grep -q "^$3	" "$SESSIONS_FILE" 2>/dev/null && exit 0; exit 1 ;;
    list-sessions) [ -f "$SESSIONS_FILE" ] && awk -F'\t' '{print $1}' "$SESSIONS_FILE"; exit 0 ;;
    list-clients) [ -f "$SESSIONS_FILE" ] && awk -F'\t' '$3 == "true" {print $1}' "$SESSIONS_FILE"; exit 0 ;;
    display-message) echo "0"; exit 0 ;;
    new-session|send-keys|kill-session|attach-session) exit 0 ;;
    *) exit 0 ;;
esac
STUBEOF
chmod +x %s/stubs/tmux
`, rootDir, rootDir, rootDir))

	return &sshTestEnv{t: t, host: host, rootDir: rootDir, repo: repo, stubDir: rootDir + "/stubs"}
}

func (e *sshTestEnv) addWorktree(name string) string {
	e.t.Helper()
	repoDir := filepath.Dir(e.repo)
	wtDir := repoDir + "/" + name
	sshRun(e.t, e.host, fmt.Sprintf(
		"cd %s && git worktree add %s -b %s",
		e.repo, wtDir, name))
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

func (e *sshTestEnv) wt(args ...string) string {
	e.t.Helper()
	cmd := exec.Command(wtBinary, args...)
	cmd.Env = append(os.Environ(),
		"HOME="+e.rootDir,
		"WT_REMOTE_HOST="+e.host,
		"WT_TEST_SESSIONS="+e.rootDir+"/tmux-sessions",
		"PATH="+e.stubDir+":"+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Logf("wt %v: %v\n%s", args, err, out)
	}
	return string(out)
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

	env.addWorktree("ssh-clean")

	wt2 := env.addWorktree("ssh-dirty")
	sshRun(t, env.host, fmt.Sprintf("echo dirty > %s/dirty.txt", wt2))

	wt3 := env.addWorktree("ssh-unpushed")
	env.commitFile(wt3, "a.txt", "a", "unpushed")

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

func TestSSH_Ls_BadHost(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping SSH e2e test in short mode")
	}

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

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Errorf("expected exit 0, got %d; output:\n%s", exitErr.ExitCode(), output)
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if !strings.Contains(output, "warning") {
		t.Error("expected a warning about unreachable remote host in output")
	}
}

func TestCreate_Remote(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping SSH e2e test in short mode")
	}

	host := "localhost"
	if out, err := sshRunErr(host, "echo ok"); err != nil {
		t.Skipf("SSH to %s not available: %v\n%s", host, err, out)
	}

	name := fmt.Sprintf("wt-e2e-create-%d-%d", time.Now().UnixNano(), rand.Intn(100000))
	remoteHome := strings.TrimSpace(sshRun(t, host, `cd "$HOME" && pwd -P`))
	rootDir := remoteHome + "/" + name
	repo := rootDir + "/repo"
	stubDir := rootDir + "/stubs"

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

	// Write stub tmux on remote
	sshRun(t, host, fmt.Sprintf(`
mkdir -p %s
cat > %s/tmux << 'STUBEOF'
#!/bin/sh
SESSIONS_FILE="$WT_TEST_SESSIONS"
case "$1" in
    has-session) exit 1 ;;
    new-session)
        session=""; dir=""; prev=""
        for arg in "$@"; do
            case "$prev" in -s) session="$arg" ;; -c) dir="$arg" ;; esac
            prev="$arg"
        done
        [ -n "$session" ] && echo "${session}	${dir}	false	false" >> "$SESSIONS_FILE"
        ;;
    send-keys|kill-session|attach-session) exit 0 ;;
    *) exit 0 ;;
esac
STUBEOF
chmod +x %s/tmux
`, stubDir, stubDir, stubDir))

	t.Cleanup(func() { sshRun(t, host, "rm -rf "+rootDir) })

	// Create a local stubs dir with the same stub tmux
	localStubDir := t.TempDir()
	localTmux := filepath.Join(localStubDir, "tmux")
	os.WriteFile(localTmux, []byte(`#!/bin/sh
case "$1" in
    attach-session) exit 0 ;;
    *) exit 0 ;;
esac
`), 0755)

	sessionsFile := filepath.Join(t.TempDir(), "tmux-sessions")

	cmd := exec.Command(wtBinary, host+":"+repo)
	cmd.Env = append(os.Environ(),
		"HOME="+remoteHome,
		"WT_REMOTE_HOST=",
		"WT_TEST_SESSIONS="+sessionsFile,
		"PATH="+localStubDir+":"+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	output := string(out)
	t.Logf("wt host:path output:\n%s", output)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("expected exit 0, got %d; output:\n%s", exitErr.ExitCode(), output)
		} else {
			t.Fatalf("unexpected error: %v\n%s", err, output)
		}
	}

	repoBase := filepath.Base(repo)
	nameRe := regexp.MustCompile(regexp.QuoteMeta(repoBase) + `-[0-9a-f]{7}`)
	match := nameRe.FindString(output)
	if match == "" {
		t.Fatalf("output does not contain a worktree name matching %s-<7hex>:\n%s", repoBase, output)
	}

	repoDir := filepath.Dir(repo)
	wtDir := repoDir + "/" + match
	if _, sshErr := sshRunErr(host, fmt.Sprintf("test -d '%s'", wtDir)); sshErr != nil {
		t.Errorf("worktree directory %s was not created on remote", wtDir)
	}
}
