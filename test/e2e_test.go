package e2e_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var wtBinary string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "wt-e2e-bin-")
	if err != nil {
		panic(err)
	}
	wtBinary = filepath.Join(tmp, "wt")
	cmd := exec.Command("go", "build", "-o", wtBinary, ".")
	cmd.Dir = filepath.Join(mustCwd(), "..")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("failed to build wt: " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

func mustCwd() string {
	d, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return d
}

type testEnv struct {
	t          *testing.T
	rootDir    string
	dataDir    string
	repo       string
	mockURL    string
	mockPort   string
	sessions   []mockSession
	sessionMu  sync.Mutex
}

type mockSession struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
	Time      struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	rootDir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(rootDir); err == nil {
		rootDir = resolved
	}

	dataDir := filepath.Join(rootDir, "data")
	os.MkdirAll(dataDir, 0755)

	bare := filepath.Join(rootDir, "origin.git")
	gitCmd(t, "", "init", "--bare", bare)

	repo := filepath.Join(rootDir, "repo")
	gitCmd(t, "", "clone", bare, repo)
	gitCmd(t, repo, "config", "user.email", "test@test.com")
	gitCmd(t, repo, "config", "user.name", "Test")
	gitCmd(t, repo, "commit", "--allow-empty", "-m", "initial")
	gitCmd(t, repo, "push", "origin", "main")

	env := &testEnv{t: t, rootDir: rootDir, dataDir: dataDir, repo: repo}
	env.startMockServer()
	return env
}

func (e *testEnv) addWorktree(name string) string {
	e.t.Helper()
	wtDir := filepath.Join(e.repo, ".worktrees", name)
	gitCmd(e.t, e.repo, "worktree", "add", wtDir, "-b", name)
	return wtDir
}

func (e *testEnv) commitFile(dir, filename, content, msg string) {
	e.t.Helper()
	os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644)
	gitCmd(e.t, dir, "add", filename)
	gitCmd(e.t, dir, "commit", "-m", msg)
}

func (e *testEnv) push(branch string) {
	e.t.Helper()
	gitCmd(e.t, e.repo, "push", "origin", branch)
}

func (e *testEnv) mergeToMain(branch string) {
	e.t.Helper()
	gitCmd(e.t, e.repo, "checkout", "main")
	gitCmd(e.t, e.repo, "merge", "--no-ff", branch, "-m", "merge "+branch)
	gitCmd(e.t, e.repo, "push", "origin", "main")
	gitCmd(e.t, e.repo, "fetch", "origin")
}

func (e *testEnv) squashMergeToMain(branch string) {
	e.t.Helper()
	gitCmd(e.t, e.repo, "checkout", "main")
	gitCmd(e.t, e.repo, "merge", "--squash", branch)
	gitCmd(e.t, e.repo, "commit", "-m", "squash merge "+branch)
	gitCmd(e.t, e.repo, "push", "origin", "main")
	gitCmd(e.t, e.repo, "fetch", "origin")
}

func (e *testEnv) createSession(dir string) {
	e.t.Helper()
	now := time.Now().UnixMilli()
	e.sessionMu.Lock()
	e.sessions = append(e.sessions, mockSession{
		ID:        fmt.Sprintf("ses_test_%d", len(e.sessions)),
		Directory: dir,
		Title:     "Test instruction compliance",
		Time: struct {
			Created int64 `json:"created"`
			Updated int64 `json:"updated"`
		}{Created: now, Updated: now},
	})
	e.sessionMu.Unlock()
}

func (e *testEnv) createIdleSession(dir string) {
	e.t.Helper()
	now := time.Now()
	idle := now.Add(-1 * time.Hour)
	e.sessionMu.Lock()
	e.sessions = append(e.sessions, mockSession{
		ID:        fmt.Sprintf("ses_test_%d", len(e.sessions)),
		Directory: dir,
		Title:     "Test instruction compliance",
		Time: struct {
			Created int64 `json:"created"`
			Updated int64 `json:"updated"`
		}{Created: idle.UnixMilli(), Updated: idle.UnixMilli()},
	})
	e.sessionMu.Unlock()
}

func (e *testEnv) startMockServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/global/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"healthy": true})
	})
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		e.sessionMu.Lock()
		sessions := make([]mockSession, len(e.sessions))
		copy(sessions, e.sessions)
		e.sessionMu.Unlock()

		dir := r.URL.Query().Get("directory")
		if dir != "" {
			var filtered []mockSession
			for _, s := range sessions {
				if s.Directory == dir {
					filtered = append(filtered, s)
				}
			}
			sessions = filtered
		}
		json.NewEncoder(w).Encode(sessions)
	})
	mux.HandleFunc("/session/", func(w http.ResponseWriter, r *http.Request) {
		// Extract session ID from /session/<id>/message and look up
		// whether the session is active (recent UpdatedAt) or idle.
		// Return a streaming message (completed=0) for active sessions
		// and a completed message for idle sessions.
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		var sessionID string
		if len(parts) >= 2 {
			sessionID = parts[1]
		}

		completed := 1 // default: completed (idle)
		e.sessionMu.Lock()
		for _, s := range e.sessions {
			if s.ID == sessionID {
				age := time.Since(time.UnixMilli(s.Time.Updated))
				if age < 30*time.Second {
					completed = 0 // streaming (active)
				}
				break
			}
		}
		e.sessionMu.Unlock()

		type msgInfo struct {
			Role   string         `json:"role"`
			Tokens map[string]int `json:"tokens"`
			Time   map[string]int `json:"time"`
		}
		type msg struct {
			Info msgInfo `json:"info"`
		}
		messages := []msg{
			{Info: msgInfo{
				Role:   "assistant",
				Tokens: map[string]int{"total": 0},
				Time:   map[string]int{"completed": completed},
			}},
		}
		json.NewEncoder(w).Encode(messages)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	e.t.Cleanup(func() { srv.Close() })
	e.mockURL = "http://" + ln.Addr().String()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	e.mockPort = port
}

func (e *testEnv) wt(args ...string) string {
	e.t.Helper()
	cmd := exec.Command(wtBinary, args...)
	cmd.Dir = e.repo
	cmd.Env = append(os.Environ(),
		"HOME="+e.rootDir,
		"WT_REMOTE_HOST=",
		"WT_OPENCODE_PORT="+e.mockPort,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Logf("wt %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (e *testEnv) worktreeExists(name string) bool {
	_, err := os.Stat(filepath.Join(e.repo, ".worktrees", name))
	return err == nil
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func assertContains(t *testing.T, output, substring string) {
	t.Helper()
	if !strings.Contains(output, substring) {
		t.Errorf("output does not contain %q:\n%s", substring, output)
	}
}

// --- Targeted rm tests (always removes) ---

func TestTargetedRm_Dirty(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	wt := env.addWorktree("dirty")
	os.WriteFile(filepath.Join(wt, "f.txt"), []byte("x"), 0644)

	out := env.wt("rm", "dirty")
	assertContains(t, out, "removed")
	if env.worktreeExists("dirty") {
		t.Error("targeted rm should remove dirty worktree")
	}
}

func TestTargetedRm_Unmerged(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	wt := env.addWorktree("unpushed")
	env.commitFile(wt, "a.txt", "a", "local work")

	out := env.wt("rm", "unpushed")
	assertContains(t, out, "removed")
	if env.worktreeExists("unpushed") {
		t.Error("targeted rm should remove unmerged worktree")
	}
}

// --- Workflow tests ---

func TestTargetedRm_CleanNoSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	env.addWorktree("clean")

	out := env.wt("rm", "clean")
	assertContains(t, out, "clean")
	assertContains(t, out, "removed")
	if env.worktreeExists("clean") {
		t.Error("clean no-session worktree should have been removed")
	}
}

func TestTargetedRm_MergedBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	wt := env.addWorktree("merged")
	env.commitFile(wt, "f.txt", "done", "feature")
	env.push("merged")
	env.mergeToMain("merged")

	out := env.wt("rm", "merged")
	assertContains(t, out, "merged")
	assertContains(t, out, "removed")
	if env.worktreeExists("merged") {
		t.Error("merged worktree should have been removed")
	}
}

func TestTargetedRm_PushedUnmerged(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	wt := env.addWorktree("in-review")
	env.commitFile(wt, "f.txt", "wip", "work")
	env.push("in-review")

	out := env.wt("rm", "in-review")
	assertContains(t, out, "removed")
	if env.worktreeExists("in-review") {
		t.Error("targeted rm should remove pushed unmerged worktree")
	}
}

// --- Batch tests ---

func TestLs_UnifiedStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// empty *: clean, no session
	env.addWorktree("batch-clean")

	// empty *: regular merge (commits become ancestors of main → unique=0, no session)
	wt2 := env.addWorktree("batch-merged")
	env.commitFile(wt2, "f.txt", "done", "feature")
	env.push("batch-merged")
	env.mergeToMain("batch-merged")
	gitCmd(t, env.repo, "checkout", "main")

	// merged *: squash-merged (unique>0 but merge-tree detects content in main)
	// Uses idle session so "working" doesn't take priority over "merged".
	wt5 := env.addWorktree("batch-squashed")
	env.commitFile(wt5, "g.txt", "squashed", "squash feature")
	env.push("batch-squashed")
	env.createIdleSession(wt5)
	env.squashMergeToMain("batch-squashed")
	gitCmd(t, env.repo, "checkout", "main")

	// dirty: uncommitted changes
	wt3 := env.addWorktree("batch-dirty")
	os.WriteFile(filepath.Join(wt3, "f.txt"), []byte("x"), 0644)

	// committed: unpushed commits
	wt4 := env.addWorktree("batch-unpushed")
	env.commitFile(wt4, "a.txt", "a", "local")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	assertContains(t, out, "batch-clean")
	assertContains(t, out, "batch-merged")
	assertContains(t, out, "batch-squashed")
	assertContains(t, out, "empty *")

	// Squash-merged branch has an idle session and unique commits, but merge-tree
	// detection recognizes its changes are in main — classified as "merged".
	// Without squash detection it would be "committed".
	if !strings.Contains(out, "batch-squashed") || !strings.Contains(out, "merged *") {
		t.Error("squash-merged worktree should be classified as merged *")
	}

	assertContains(t, out, "batch-dirty")
	assertContains(t, out, "dirty")
	assertContains(t, out, "batch-unpushed")
	assertContains(t, out, "committed")
}

// TestLs_RegressionPrunedTrackingRef verifies that squash merge detection
// works even when the remote tracking ref (refs/remotes/origin/<branch>) has
// been pruned. Previously IsMerged gated on the tracking ref existing, so
// fetch.prune=true would cause merged branches to be classified as "committed".
func TestLs_RegressionPrunedTrackingRef(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// Create a branch, push, squash-merge, then prune the tracking ref
	wt := env.addWorktree("pruned-ref")
	env.commitFile(wt, "h.txt", "pruned", "pruned feature")
	env.push("pruned-ref")
	env.createIdleSession(wt)
	env.squashMergeToMain("pruned-ref")
	gitCmd(t, env.repo, "checkout", "main")

	// Simulate fetch.prune=true deleting the tracking ref
	gitCmd(t, env.repo, "update-ref", "-d", "refs/remotes/origin/pruned-ref")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	if !strings.Contains(out, "pruned-ref") || !strings.Contains(out, "merged *") {
		t.Error("squash-merged worktree with pruned tracking ref should be classified as merged *")
	}
}

// TestLs_RegressionMergeTreeConflict verifies that squash merge detection
// works when git merge-tree produces conflicts. This happens when main has
// moved forward and later commits touch the same files the branch modified.
// The merge-tree simulation (Phase 2) fails with conflicts, but the patch-id
// comparison (Phase 3) correctly identifies the squash merge.
func TestLs_RegressionMergeTreeConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// Create a branch that modifies a file
	wt := env.addWorktree("conflict-branch")
	env.commitFile(wt, "shared.txt", "branch content", "branch change")
	env.push("conflict-branch")
	env.createIdleSession(wt)
	env.squashMergeToMain("conflict-branch")
	gitCmd(t, env.repo, "checkout", "main")

	// Now add more commits to main that modify the same file, causing
	// merge-tree conflicts when it tries to simulate merging the branch.
	os.WriteFile(filepath.Join(env.repo, "shared.txt"), []byte("later main content"), 0644)
	gitCmd(t, env.repo, "add", "shared.txt")
	gitCmd(t, env.repo, "commit", "-m", "main moves forward on same file")
	gitCmd(t, env.repo, "push", "origin", "main")
	gitCmd(t, env.repo, "fetch", "origin")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	if !strings.Contains(out, "conflict-branch") || !strings.Contains(out, "merged *") {
		t.Error("squash-merged worktree with merge-tree conflicts should be classified as merged * via patch-id fallback")
	}
}

// TestLs_RegressionMultiCommitSquash verifies that patch-id detection works
// for branches with multiple commits that are squash-merged into a single
// commit on main, and where merge-tree produces conflicts.
func TestLs_RegressionMultiCommitSquash(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// Create a branch with multiple commits
	wt := env.addWorktree("multi-commit")
	env.commitFile(wt, "a.txt", "first change", "commit 1")
	env.commitFile(wt, "b.txt", "second change", "commit 2")
	env.commitFile(wt, "c.txt", "third change", "commit 3")
	env.push("multi-commit")
	env.createIdleSession(wt)
	env.squashMergeToMain("multi-commit")
	gitCmd(t, env.repo, "checkout", "main")

	// Add a conflicting change on main to force Phase 3
	os.WriteFile(filepath.Join(env.repo, "a.txt"), []byte("later main content"), 0644)
	gitCmd(t, env.repo, "add", "a.txt")
	gitCmd(t, env.repo, "commit", "-m", "main moves forward on same file")
	gitCmd(t, env.repo, "push", "origin", "main")
	gitCmd(t, env.repo, "fetch", "origin")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	if !strings.Contains(out, "multi-commit") || !strings.Contains(out, "merged *") {
		t.Error("multi-commit squash-merged worktree should be classified as merged * via patch-id fallback")
	}
}

func TestLs_SessionActiveStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	wt := env.addWorktree("batch-active")
	env.createSession(wt)

	out := env.wt("ls")
	t.Log("output:\n" + out)

	// Session is recent with no commits — shown as working
	assertContains(t, out, "batch-active")
	assertContains(t, out, "working")
}

func TestBatchRm(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// empty * → removed
	env.addWorktree("rm-empty")

	// dirty → kept
	wt2 := env.addWorktree("rm-dirty")
	os.WriteFile(filepath.Join(wt2, "f.txt"), []byte("x"), 0644)

	out := env.wt("rm")
	t.Log("output:\n" + out)

	assertContains(t, out, "rm-empty")
	assertContains(t, out, "removed")

	if !env.worktreeExists("rm-dirty") {
		t.Error("dirty worktree should not be removed")
	}
	if env.worktreeExists("rm-empty") {
		t.Error("empty worktree should have been removed")
	}
}

// --- Remote host configuration tests ---

// wtRaw runs the wt binary with explicit env overrides, returning combined output and exit code.
func wtRaw(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(wtBinary, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return string(out), exitErr.ExitCode()
		}
		t.Fatalf("unexpected error: %v", err)
	}
	return string(out), 0
}

func TestRemote_HostNotSet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()

	base := []string{"WT_REMOTE_HOST=", "HOME=" + t.TempDir()}

	out, code := wtRaw(t, base, "-r", "ls")
	if code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	assertContains(t, out, "WT_REMOTE_HOST is not set")
	assertContains(t, out, "export WT_REMOTE_HOST=")

	out, code = wtRaw(t, base, "-r", "/tmp/fake")
	if code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	assertContains(t, out, "WT_REMOTE_HOST is not set")
	assertContains(t, out, "export WT_REMOTE_HOST=")
}

func TestRemote_HostUnreachable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()

	base := []string{"WT_REMOTE_HOST=wt-nonexistent-host-test", "HOME=" + t.TempDir()}

	out, code := wtRaw(t, base, "-r", "ls")
	if code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	assertContains(t, out, "cannot connect to remote host")

	out, code = wtRaw(t, base, "-r", "/tmp/fake")
	if code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	assertContains(t, out, "cannot resolve remote HOME")
}

// --- Diff tests ---

func TestDiff_CommittedChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("diff-test")
	env.commitFile(wt, "feature.txt", "new feature content", "add feature")

	out := env.wt("diff", "diff-test")
	t.Log("output:\n" + out)

	// Stat summary should list the changed file
	assertContains(t, out, "feature.txt")
	// Full diff should contain the file content
	assertContains(t, out, "new feature content")
}

func TestDiff_NoChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	env.addWorktree("diff-empty")

	out := env.wt("diff", "diff-empty")
	t.Log("output:\n" + out)

	assertContains(t, out, "No changes on this branch.")
}

func TestDiff_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	out := env.wt("diff", "nonexistent")
	t.Log("output:\n" + out)

	assertContains(t, out, "not found")
}
