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
	Tokens int // tokens returned in the message endpoint (not serialized to session list)
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
	// New default layout: <parent>/<name> (root="..")
	wtDir := filepath.Join(filepath.Dir(e.repo), name)
	gitCmd(e.t, e.repo, "worktree", "add", wtDir, "-b", name)
	// Set upstream tracking to match WorktreeAdd behavior.
	rootBranch := strings.TrimSpace(gitCmd(e.t, e.repo, "rev-parse", "--abbrev-ref", "HEAD"))
	gitCmd(e.t, e.repo, "branch", "--set-upstream-to", "origin/"+rootBranch, name)
	return wtDir
}

// addLegacySiblingWorktree creates a worktree in the old sibling layout:
// <parent>/<repobase>-<name>. Used to test backward-compat discovery.
func (e *testEnv) addLegacySiblingWorktree(name string) string {
	e.t.Helper()
	wtDir := filepath.Join(filepath.Dir(e.repo), filepath.Base(e.repo)+"-"+name)
	gitCmd(e.t, e.repo, "worktree", "add", wtDir, "-b", name)
	rootBranch := strings.TrimSpace(gitCmd(e.t, e.repo, "rev-parse", "--abbrev-ref", "HEAD"))
	gitCmd(e.t, e.repo, "branch", "--set-upstream-to", "origin/"+rootBranch, name)
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

func (e *testEnv) createSessionWithTokens(dir string, tokens int) {
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
		Tokens: tokens,
	})
	e.sessionMu.Unlock()
}

func (e *testEnv) createStaleSession(dir string, tokens int) {
	e.t.Helper()
	staleTime := time.Now().Add(-5 * time.Hour)
	e.sessionMu.Lock()
	e.sessions = append(e.sessions, mockSession{
		ID:        fmt.Sprintf("ses_test_%d", len(e.sessions)),
		Directory: dir,
		Title:     "Stale session",
		Time: struct {
			Created int64 `json:"created"`
			Updated int64 `json:"updated"`
		}{Created: staleTime.UnixMilli(), Updated: staleTime.UnixMilli()},
		Tokens: tokens,
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
		tokens := 0
		e.sessionMu.Lock()
		for _, s := range e.sessions {
			if s.ID == sessionID {
				age := time.Since(time.UnixMilli(s.Time.Updated))
				if age < 30*time.Second {
					completed = 0 // streaming (active)
				}
				tokens = s.Tokens
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
				Tokens: map[string]int{"total": tokens},
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
		e.t.Fatalf("wt %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (e *testEnv) wtWithExit(args ...string) (string, int) {
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
		if exitErr, ok := err.(*exec.ExitError); ok {
			return string(out), exitErr.ExitCode()
		}
		e.t.Fatalf("unexpected error: %v", err)
	}
	return string(out), 0
}

func (e *testEnv) worktreeExists(name string) bool {
	// New default layout: <parent>/<name>
	_, err := os.Stat(filepath.Join(filepath.Dir(e.repo), name))
	return err == nil
}

func (e *testEnv) legacySiblingWorktreeExists(name string) bool {
	_, err := os.Stat(filepath.Join(filepath.Dir(e.repo), filepath.Base(e.repo)+"-"+name))
	return err == nil
}

func (e *testEnv) childWorktreeExists(name string) bool {
	_, err := os.Stat(filepath.Join(e.repo, ".worktrees", name))
	return err == nil
}

func (e *testEnv) rootWorktreeExists(root, name string) bool {
	_, err := os.Stat(filepath.Join(e.repo, root, name))
	return err == nil
}

func (e *testEnv) branchExists(name string) bool {
	e.t.Helper()
	cmd := exec.Command("git", "-C", e.repo, "rev-parse", "--verify", "refs/heads/"+name)
	return cmd.Run() == nil
}

func (e *testEnv) addChildWorktree(name string) string {
	e.t.Helper()
	wtDir := filepath.Join(e.repo, ".worktrees", name)
	gitCmd(e.t, e.repo, "worktree", "add", wtDir, "-b", name)
	rootBranch := strings.TrimSpace(gitCmd(e.t, e.repo, "rev-parse", "--abbrev-ref", "HEAD"))
	gitCmd(e.t, e.repo, "branch", "--set-upstream-to", "origin/"+rootBranch, name)
	return wtDir
}

func (e *testEnv) addRootWorktree(root, name string) string {
	e.t.Helper()
	wtDir := filepath.Join(e.repo, root, name)
	gitCmd(e.t, e.repo, "worktree", "add", wtDir, "-b", name)
	rootBranch := strings.TrimSpace(gitCmd(e.t, e.repo, "rev-parse", "--abbrev-ref", "HEAD"))
	gitCmd(e.t, e.repo, "branch", "--set-upstream-to", "origin/"+rootBranch, name)
	return wtDir
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

// TestTargetedRm_RegressionOrphanedBranch verifies that wt rm deletes the
// branch even when the worktree directory has already been removed externally.
// Previously, git worktree remove would fail on the missing directory and the
// early return skipped git branch -D, orphaning the branch.
func TestTargetedRm_RegressionOrphanedBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	wtDir := env.addWorktree("orphan-test")

	// Simulate external deletion of the worktree directory.
	os.RemoveAll(wtDir)

	out := env.wt("rm", "orphan-test")
	assertContains(t, out, "removed")
	if env.branchExists("orphan-test") {
		t.Error("branch should be deleted even when worktree directory is already gone")
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

// TestLs_RegressionRebaseMerge verifies that merges producing zero unique
// commits are detected. When a branch is rebased onto main (identical SHAs
// adopted) or merged via --no-ff (commits become ancestors of the merge
// commit), rev-list sees zero unique commits. The behind-upstream check
// detects this: the branch tip is a proper ancestor of upstream. Requires
// a session — a worktree with no session is classified as empty, not merged.
func TestLs_RegressionRebaseMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// Create a branch with a commit
	wt := env.addWorktree("rebase-merged")
	env.commitFile(wt, "r.txt", "rebase content", "rebase work")
	env.push("rebase-merged")
	env.createIdleSession(wt)

	// Simulate rebase merge: fast-forward main to include the branch's commit
	gitCmd(t, env.repo, "checkout", "main")
	gitCmd(t, env.repo, "rebase", "rebase-merged")
	gitCmd(t, env.repo, "push", "origin", "main")
	gitCmd(t, env.repo, "fetch", "origin")

	// After rebase merge, advance main further so the branch is behind
	env.commitFile(env.repo, "extra.txt", "more main work", "main advance")
	gitCmd(t, env.repo, "push", "origin", "main")
	gitCmd(t, env.repo, "fetch", "origin")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	if !strings.Contains(out, "rebase-merged") || !strings.Contains(out, "merged *") {
		t.Error("rebase-merged worktree should be classified as merged *")
	}
}

// TestLs_RegressionRegularMergeWithSession verifies that a regular merge
// commit (--no-ff) with a session is detected as merged. After the merge,
// the branch's commits are ancestors of the merge commit on main, so
// rev-list sees zero unique commits. The behind-upstream check fires because
// the branch is behind upstream and has a session.
func TestLs_RegressionRegularMergeWithSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("regular-merged")
	env.commitFile(wt, "r.txt", "regular content", "regular work")
	env.push("regular-merged")
	env.createIdleSession(wt)

	// Regular merge commit to main
	env.mergeToMain("regular-merged")
	gitCmd(t, env.repo, "checkout", "main")

	// Advance main so the branch is behind
	env.commitFile(env.repo, "extra.txt", "main work", "main advance")
	gitCmd(t, env.repo, "push", "origin", "main")
	gitCmd(t, env.repo, "fetch", "origin")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	if !strings.Contains(out, "regular-merged") || !strings.Contains(out, "merged *") {
		t.Error("regular-merged worktree with session should be classified as merged *")
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

	out, code := wtRaw(t, base, "-r", "/tmp/fake")
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

	out, code := wtRaw(t, base, "-r", "/tmp/fake")
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

	out, code := env.wtWithExit("diff", "nonexistent")
	t.Log("output:\n" + out)

	if code == 0 {
		t.Error("expected non-zero exit code for diff nonexistent")
	}
	assertContains(t, out, "not found")
}

// TestDiff_NonDefaultBranch verifies that wt diff uses the upstream tracking
// ref, not the repo's default branch. Reproduces the bug where a worktree
// branched from a non-default branch (e.g., krocodile) would diff against
// origin/main instead of origin/<actual-base>.
func TestDiff_NonDefaultBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// Create a non-default branch "krocodile" with its own commits
	gitCmd(t, env.repo, "checkout", "-b", "krocodile")
	env.commitFile(env.repo, "kroc.txt", "krocodile content", "krocodile base")
	gitCmd(t, env.repo, "push", "origin", "krocodile")

	// Now create a worktree from krocodile (repo root is on krocodile)
	wt := env.addWorktree("feature-on-kroc")
	env.commitFile(wt, "feature.txt", "new feature", "add feature on krocodile")

	out := env.wt("diff", "feature-on-kroc")
	t.Log("output:\n" + out)

	// Should show feature.txt (the worktree's change) but NOT kroc.txt
	// (which is on krocodile, the base branch)
	assertContains(t, out, "feature.txt")
	if strings.Contains(out, "kroc.txt") {
		t.Error("diff should be against origin/krocodile (upstream), not origin/main; kroc.txt should not appear")
	}
}

// TestLs_NonDefaultBranch verifies that wt ls correctly classifies worktrees
// branched from a non-default branch using the upstream tracking ref.
func TestLs_NonDefaultBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// Create a non-default branch "krocodile" with its own commits
	gitCmd(t, env.repo, "checkout", "-b", "krocodile")
	env.commitFile(env.repo, "kroc.txt", "krocodile content", "krocodile base")
	gitCmd(t, env.repo, "push", "origin", "krocodile")

	// Create a worktree from krocodile, commit, push, and merge back
	wt := env.addWorktree("kroc-merged")
	env.commitFile(wt, "f.txt", "done", "feature")
	gitCmd(t, env.repo, "push", "origin", "kroc-merged")
	// Merge into krocodile (not main)
	gitCmd(t, env.repo, "checkout", "krocodile")
	gitCmd(t, env.repo, "merge", "--no-ff", "kroc-merged", "-m", "merge kroc-merged")
	gitCmd(t, env.repo, "push", "origin", "krocodile")
	gitCmd(t, env.repo, "fetch", "origin")

	env.createIdleSession(wt)

	out := env.wt("ls")
	t.Log("output:\n" + out)

	// Should be classified as merged (not committed) since it's merged into krocodile
	if !strings.Contains(out, "kroc-merged") {
		t.Error("worktree should appear in ls output")
	}
	// The branch is merged into origin/krocodile (its upstream), so it must NOT
	// be classified as "committed". With the old DefaultBranch code, it would
	// show as "committed" because origin/main doesn't contain the krocodile
	// commits.
	if strings.Contains(out, "committed") {
		t.Error("worktree should not be classified as committed; upstream tracking ref is wrong")
	}
}

// --- Child layout tests ---

func TestLs_ChildLayout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// Create worktrees in both layouts
	env.addWorktree("sibling-wt")
	env.addChildWorktree("child-wt")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	assertContains(t, out, "sibling-wt")
	assertContains(t, out, "child-wt")
}

func TestTargetedRm_ChildLayout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	env.addChildWorktree("child-rm")

	out := env.wt("rm", "child-rm")
	assertContains(t, out, "removed")
	if env.childWorktreeExists("child-rm") {
		t.Error("child layout worktree should have been removed")
	}
}

func TestBatchRm_ChildLayout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// empty * in child layout → removed
	env.addChildWorktree("child-empty")

	// dirty in child layout → kept
	wt := env.addChildWorktree("child-dirty")
	os.WriteFile(filepath.Join(wt, "f.txt"), []byte("x"), 0644)

	out := env.wt("rm")
	t.Log("output:\n" + out)

	assertContains(t, out, "child-empty")
	assertContains(t, out, "removed")

	if !env.childWorktreeExists("child-dirty") {
		t.Error("dirty child worktree should not be removed")
	}
	if env.childWorktreeExists("child-empty") {
		t.Error("empty child worktree should have been removed")
	}
}

// --- Custom root tests ---

func TestLs_CustomRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// Create worktree using a custom root
	env.addRootWorktree(".wt", "custom-root-wt")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	assertContains(t, out, "custom-root-wt")
}

func TestTargetedRm_CustomRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	env.addRootWorktree(".wt", "custom-rm")

	out := env.wt("rm", "custom-rm")
	assertContains(t, out, "removed")
	if env.rootWorktreeExists(".wt", "custom-rm") {
		t.Error("custom root worktree should have been removed")
	}
}

// --- Legacy sibling layout compat tests ---

func TestLs_LegacySiblingCompat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// Create one worktree in the old <repobase>-<name> layout and one in new layout
	env.addLegacySiblingWorktree("old-sibling")
	env.addWorktree("new-default")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	assertContains(t, out, "old-sibling")
	assertContains(t, out, "new-default")
}

func TestTargetedRm_LegacySiblingCompat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	env.addLegacySiblingWorktree("old-rm")

	out := env.wt("rm", "old-rm")
	assertContains(t, out, "removed")
	if env.legacySiblingWorktreeExists("old-rm") {
		t.Error("legacy sibling worktree should have been removed")
	}
}

// --- Token formatting in ls output ---

func TestLs_TokenFormatting(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// Small tokens: 500 → "500"
	wt1 := env.addWorktree("tok-small")
	env.createSessionWithTokens(wt1, 500)

	// Medium tokens: 15000 → "15k"
	wt2 := env.addWorktree("tok-medium")
	env.createSessionWithTokens(wt2, 15000)

	// Large tokens: 1500000 → "1.5M"
	wt3 := env.addWorktree("tok-large")
	env.createSessionWithTokens(wt3, 1500000)

	// Kilo with decimal: 1500 → "1.5k"
	wt4 := env.addWorktree("tok-kilo")
	env.createSessionWithTokens(wt4, 1500)

	out := env.wt("ls")
	t.Log("output:\n" + out)

	// Verify each worktree appears with the correctly formatted token string.
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "tok-small") {
			assertContains(t, line, "500")
		}
		if strings.Contains(line, "tok-medium") {
			assertContains(t, line, "15k")
		}
		if strings.Contains(line, "tok-large") {
			assertContains(t, line, "1.5M")
		}
		if strings.Contains(line, "tok-kilo") {
			assertContains(t, line, "1.5k")
		}
	}
}

// --- Session status: working (streaming detection) ---

func TestLs_WorkingStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// createSession uses time.Now() → UpdatedAt is recent → mock returns completed=0 → streaming
	wt := env.addWorktree("status-working")
	env.createSession(wt)

	out := env.wt("ls")
	t.Log("output:\n" + out)

	assertContains(t, out, "status-working")
	assertContains(t, out, "working")
}

// --- Session status: stale ---

func TestLs_StaleStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// Stale session: UpdatedAt > 4h ago, no unique commits
	wt := env.addWorktree("status-stale")
	env.createStaleSession(wt, 42000)

	out := env.wt("ls")
	t.Log("output:\n" + out)

	assertContains(t, out, "status-stale")
	assertContains(t, out, "stale")
	// Stale sessions must still show tokens
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "status-stale") {
			assertContains(t, line, "42k")
		}
	}
}

// --- Sort ordering ---

func TestLs_SortOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// Create worktrees with different activity times.
	// "sort-recent" has a session updated 30min ago → most recent activity
	wt1 := env.addWorktree("sort-recent")
	now := time.Now()
	env.sessionMu.Lock()
	env.sessions = append(env.sessions, mockSession{
		ID:        fmt.Sprintf("ses_test_%d", len(env.sessions)),
		Directory: wt1,
		Title:     "Recent work",
		Time: struct {
			Created int64 `json:"created"`
			Updated int64 `json:"updated"`
		}{Created: now.Add(-1 * time.Hour).UnixMilli(), Updated: now.Add(-30 * time.Minute).UnixMilli()},
		Tokens: 1000,
	})
	env.sessionMu.Unlock()

	// "sort-old" has a session updated 3h ago → older activity
	wt2 := env.addWorktree("sort-old")
	env.sessionMu.Lock()
	env.sessions = append(env.sessions, mockSession{
		ID:        fmt.Sprintf("ses_test_%d", len(env.sessions)),
		Directory: wt2,
		Title:     "Old work",
		Time: struct {
			Created int64 `json:"created"`
			Updated int64 `json:"updated"`
		}{Created: now.Add(-4 * time.Hour).UnixMilli(), Updated: now.Add(-3 * time.Hour).UnixMilli()},
		Tokens: 2000,
	})
	env.sessionMu.Unlock()

	// "sort-none" has no session → sorted after those with activity
	env.addWorktree("sort-none")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	// Find the positions of each worktree in the output
	recentIdx := strings.Index(out, "sort-recent")
	oldIdx := strings.Index(out, "sort-old")
	noneIdx := strings.Index(out, "sort-none")

	if recentIdx == -1 || oldIdx == -1 || noneIdx == -1 {
		t.Fatalf("expected all worktrees in output")
	}
	if recentIdx > oldIdx {
		t.Error("sort-recent should appear before sort-old (more recent activity first)")
	}
	if oldIdx > noneIdx {
		t.Error("sort-old should appear before sort-none (activity before no-activity)")
	}
}

// --- Name generation format ---

func TestLs_NameFormat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// addWorktree creates branches with the exact name passed in.
	// The real `wt` command uses GenerateName("<project>-<7hex>") to create names.
	// Verify the branch name shows up correctly in ls output.
	env.addWorktree("my-feature-abc1234")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	assertContains(t, out, "my-feature-abc1234")
}

// --- Error paths ---

func TestRm_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	out, code := env.wtWithExit("rm", "nonexistent-name")
	t.Log("output:\n" + out)

	if code == 0 {
		t.Error("expected non-zero exit code for rm nonexistent-name")
	}
	assertContains(t, out, "not found")
}

func TestDiff_MissingName(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	out, code := env.wtWithExit("diff")
	t.Log("output:\n" + out)

	if code == 0 {
		t.Error("expected non-zero exit code for diff without name argument")
	}
}

func TestRm_ExtraArgs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	out, code := env.wtWithExit("rm", "extra-arg", "another-arg")
	t.Log("output:\n" + out)

	if code == 0 {
		t.Error("expected non-zero exit code for rm with extra arguments")
	}
	assertContains(t, out, "unexpected argument")
}
