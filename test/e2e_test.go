package e2e_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
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
	t       *testing.T
	rootDir string
	repo    string
	stubDir string // directory containing mock opencode binary
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
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

	// Add .opencode to .gitignore as part of initial commit so it never
	// appears as untracked in any worktree.
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".opencode/\n"), 0644)
	gitCmd(t, repo, "add", ".gitignore")
	gitCmd(t, repo, "commit", "-m", "initial")
	gitCmd(t, repo, "push", "origin", "main")

	env := &testEnv{t: t, rootDir: rootDir, repo: repo}
	env.stubDir = env.writeStubOpenCode()
	return env
}

// writeStubOpenCode creates a mock opencode script that creates .opencode/ dir and exits.
func (e *testEnv) writeStubOpenCode() string {
	dir := filepath.Join(e.rootDir, "stubs")
	os.MkdirAll(dir, 0755)

	stub := filepath.Join(dir, "opencode")
	script := `#!/bin/sh
# Mock opencode for wt e2e tests.
# Creates .opencode/ directory to simulate session creation, then exits.
mkdir -p .opencode
touch .opencode/session
exit 0
`
	os.WriteFile(stub, []byte(script), 0755)
	return dir
}

// simulateSession creates a .opencode/ directory in the worktree to simulate
// that opencode was previously run there.
func (e *testEnv) simulateSession(wtDir string) {
	e.t.Helper()
	ocDir := filepath.Join(wtDir, ".opencode")
	os.MkdirAll(ocDir, 0755)
	os.WriteFile(filepath.Join(ocDir, "session"), []byte("mock"), 0644)
}

// simulateStaleSession creates a .opencode/ directory with old mtime.
func (e *testEnv) simulateStaleSession(wtDir string) {
	e.t.Helper()
	ocDir := filepath.Join(wtDir, ".opencode")
	os.MkdirAll(ocDir, 0755)
	sessionFile := filepath.Join(ocDir, "session")
	os.WriteFile(sessionFile, []byte("mock"), 0644)
	// Set mtime to 5 hours ago (past the 4-hour stale threshold)
	old := time.Now().Add(-5 * time.Hour)
	os.Chtimes(sessionFile, old, old)
	os.Chtimes(ocDir, old, old)
}

func (e *testEnv) addWorktree(name string) string {
	e.t.Helper()
	wtDir := filepath.Join(filepath.Dir(e.repo), name)
	gitCmd(e.t, e.repo, "worktree", "add", wtDir, "-b", name)
	return wtDir
}

func (e *testEnv) addLegacySiblingWorktree(name string) string {
	e.t.Helper()
	wtDir := filepath.Join(filepath.Dir(e.repo), filepath.Base(e.repo)+"-"+name)
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

func (e *testEnv) wt(args ...string) string {
	e.t.Helper()
	cmd := exec.Command(wtBinary, args...)
	cmd.Dir = e.repo
	cmd.Env = append(os.Environ(),
		"HOME="+e.rootDir,
		"PATH="+e.stubDir+":"+filepath.Dir(wtBinary)+":"+os.Getenv("PATH"),
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
		"PATH="+e.stubDir+":"+filepath.Dir(wtBinary)+":"+os.Getenv("PATH"),
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
	return wtDir
}

func (e *testEnv) addRootWorktree(root, name string) string {
	e.t.Helper()
	wtDir := filepath.Join(e.repo, root, name)
	gitCmd(e.t, e.repo, "worktree", "add", wtDir, "-b", name)
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

// --- Targeted rm tests ---

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

func TestTargetedRm_RegressionOrphanedBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	wtDir := env.addWorktree("orphan-test")

	os.RemoveAll(wtDir)

	out := env.wt("rm", "orphan-test")
	assertContains(t, out, "removed")
	if env.branchExists("orphan-test") {
		t.Error("branch should be deleted even when worktree directory is already gone")
	}
}

// --- Batch tests ---

func TestLs_RegressionDetachedHead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wtDir := env.addWorktree("detached-wt")
	env.commitFile(wtDir, "f.txt", "work", "add file")

	head := strings.TrimSpace(gitCmd(t, wtDir, "rev-parse", "HEAD"))
	gitCmd(t, wtDir, "checkout", head)

	out := env.wt("ls")
	t.Log("output:\n" + out)

	assertContains(t, out, "detached-wt")
}

func TestTargetedRm_RegressionDetachedHead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wtDir := env.addWorktree("detached-rm")

	head := strings.TrimSpace(gitCmd(t, wtDir, "rev-parse", "HEAD"))
	gitCmd(t, wtDir, "checkout", head)

	out := env.wt("rm", "detached-rm")
	assertContains(t, out, "removed")
	if env.worktreeExists("detached-rm") {
		t.Error("detached HEAD worktree should have been removed")
	}
}

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

	// merged *: squash-merged with session (.opencode/ dir)
	wt5 := env.addWorktree("batch-squashed")
	env.commitFile(wt5, "g.txt", "squashed", "squash feature")
	env.push("batch-squashed")
	env.simulateSession(wt5)
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

	if !strings.Contains(out, "batch-squashed") || !strings.Contains(out, "merged *") {
		t.Error("squash-merged worktree should be classified as merged *")
	}

	assertContains(t, out, "batch-dirty")
	assertContains(t, out, "dirty")
	assertContains(t, out, "batch-unpushed")
	assertContains(t, out, "committed")
}

func TestLs_MergedButDirty(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("merged-dirty")
	env.commitFile(wt, "feature.txt", "done", "feature")
	env.push("merged-dirty")
	env.simulateSession(wt)
	env.squashMergeToMain("merged-dirty")
	gitCmd(t, env.repo, "checkout", "main")

	os.WriteFile(filepath.Join(wt, "new-work.txt"), []byte("new stuff"), 0644)

	out := env.wt("ls")
	t.Log("output:\n" + out)

	if !strings.Contains(out, "merged-dirty") {
		t.Fatal("worktree should appear in ls output")
	}
	if strings.Contains(out, "merged") && !strings.Contains(out, "dirty") {
		t.Error("worktree with uncommitted changes after merge should be 'dirty', not 'merged'")
	}
	assertContains(t, out, "dirty")
}

func TestLs_RegressionPushDashU(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("push-u-branch")
	env.commitFile(wt, "feature.txt", "done", "add feature")
	gitCmd(t, env.repo, "push", "-u", "origin", "push-u-branch")
	env.simulateSession(wt)

	env.squashMergeToMain("push-u-branch")
	gitCmd(t, env.repo, "push", "origin", "--delete", "push-u-branch")
	gitCmd(t, env.repo, "fetch", "--prune")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	if !strings.Contains(out, "push-u-branch") || !strings.Contains(out, "merged *") {
		t.Error("worktree pushed with -u should still be detected as merged after remote branch deletion")
	}
}

func TestLs_RegressionPushDashUNoFF(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("push-u-noff")
	env.commitFile(wt, "f.txt", "done", "feature")
	gitCmd(t, env.repo, "push", "-u", "origin", "push-u-noff")
	env.simulateSession(wt)

	env.mergeToMain("push-u-noff")
	gitCmd(t, env.repo, "push", "origin", "--delete", "push-u-noff")
	gitCmd(t, env.repo, "fetch", "--prune")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	if !strings.Contains(out, "push-u-noff") || !strings.Contains(out, "merged *") {
		t.Error("no-ff merged worktree pushed with -u should be detected as merged after remote branch deletion")
	}
}

func TestDiff_RegressionPushDashU(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("diff-push-u")
	env.commitFile(wt, "feature.txt", "content", "add feature")
	gitCmd(t, env.repo, "push", "-u", "origin", "diff-push-u")
	gitCmd(t, env.repo, "push", "origin", "--delete", "diff-push-u")
	gitCmd(t, env.repo, "fetch", "--prune")

	out := env.wt("diff", "diff-push-u")
	t.Log("output:\n" + out)

	assertContains(t, out, "feature.txt")
}

func TestLs_RegressionPrunedTrackingRef(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("pruned-ref")
	env.commitFile(wt, "h.txt", "pruned", "pruned feature")
	env.push("pruned-ref")
	env.simulateSession(wt)
	env.squashMergeToMain("pruned-ref")
	gitCmd(t, env.repo, "checkout", "main")

	gitCmd(t, env.repo, "update-ref", "-d", "refs/remotes/origin/pruned-ref")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	if !strings.Contains(out, "pruned-ref") || !strings.Contains(out, "merged *") {
		t.Error("squash-merged worktree with pruned tracking ref should be classified as merged *")
	}
}

func TestLs_RegressionMergeTreeConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("conflict-branch")
	env.commitFile(wt, "shared.txt", "branch content", "branch change")
	env.push("conflict-branch")
	env.simulateSession(wt)
	env.squashMergeToMain("conflict-branch")
	gitCmd(t, env.repo, "checkout", "main")

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

func TestLs_RegressionMultiCommitSquash(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("multi-commit")
	env.commitFile(wt, "a.txt", "first change", "commit 1")
	env.commitFile(wt, "b.txt", "second change", "commit 2")
	env.commitFile(wt, "c.txt", "third change", "commit 3")
	env.push("multi-commit")
	env.simulateSession(wt)
	env.squashMergeToMain("multi-commit")
	gitCmd(t, env.repo, "checkout", "main")

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

func TestLs_RegressionRebaseMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("rebase-merged")
	env.commitFile(wt, "r.txt", "rebase content", "rebase work")
	env.push("rebase-merged")
	env.simulateSession(wt)

	gitCmd(t, env.repo, "checkout", "main")
	gitCmd(t, env.repo, "rebase", "rebase-merged")
	gitCmd(t, env.repo, "push", "origin", "main")
	gitCmd(t, env.repo, "fetch", "origin")

	env.commitFile(env.repo, "extra.txt", "more main work", "main advance")
	gitCmd(t, env.repo, "push", "origin", "main")
	gitCmd(t, env.repo, "fetch", "origin")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	if !strings.Contains(out, "rebase-merged") || !strings.Contains(out, "merged *") {
		t.Error("rebase-merged worktree should be classified as merged *")
	}
}

func TestLs_RegressionRegularMergeWithSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("regular-merged")
	env.commitFile(wt, "r.txt", "regular content", "regular work")
	env.push("regular-merged")
	env.simulateSession(wt)

	env.mergeToMain("regular-merged")
	gitCmd(t, env.repo, "checkout", "main")

	env.commitFile(env.repo, "extra.txt", "main work", "main advance")
	gitCmd(t, env.repo, "push", "origin", "main")
	gitCmd(t, env.repo, "fetch", "origin")

	out := env.wt("ls")
	t.Log("output:\n" + out)

	if !strings.Contains(out, "regular-merged") || !strings.Contains(out, "merged *") {
		t.Error("regular-merged worktree with session should be classified as merged *")
	}
}

func TestLs_IdleStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("session-idle")
	env.simulateSession(wt)

	out := env.wt("ls")
	t.Log("output:\n" + out)

	assertContains(t, out, "session-idle")
	// Check that the status column shows "idle" (not just the name)
	lines := strings.Split(out, "\n")
	found := false
	for _, line := range lines {
		if strings.Contains(line, "session-idle") && strings.Contains(line, "idle") {
			found = true
			break
		}
	}
	if !found {
		t.Error("worktree with .opencode/ dir and no unique commits should be classified as idle")
	}
}

func TestLs_StaleStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("stale-wt")
	env.simulateStaleSession(wt)

	out := env.wt("ls")
	t.Log("output:\n" + out)

	assertContains(t, out, "stale-wt")
	assertContains(t, out, "stale *")
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

func TestBatchRm_StaleRemoved(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("rm-stale")
	env.simulateStaleSession(wt)

	out := env.wt("rm")
	t.Log("output:\n" + out)

	assertContains(t, out, "rm-stale")
	assertContains(t, out, "removed")
	if env.worktreeExists("rm-stale") {
		t.Error("stale worktree should have been removed")
	}
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

	assertContains(t, out, "feature.txt")
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

func TestDiff_UntrackedFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("diff-untracked")
	os.WriteFile(filepath.Join(wt, "new-file.txt"), []byte("untracked content"), 0644)

	out := env.wt("diff", "diff-untracked")
	t.Log("output:\n" + out)

	assertContains(t, out, "new-file.txt")
	assertContains(t, out, "untracked content")
}

func TestLs_DirtyFromUntracked(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	wt := env.addWorktree("untracked-dirty")
	os.WriteFile(filepath.Join(wt, "new.txt"), []byte("x"), 0644)

	out := env.wt("ls")
	t.Log("output:\n" + out)

	assertContains(t, out, "untracked-dirty")
	assertContains(t, out, "dirty")

	diffOut := env.wt("diff", "untracked-dirty")
	assertContains(t, diffOut, "new.txt")
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

func TestDiff_NonDefaultBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	gitCmd(t, env.repo, "checkout", "-b", "krocodile")
	env.commitFile(env.repo, "kroc.txt", "krocodile content", "krocodile base")
	gitCmd(t, env.repo, "push", "origin", "krocodile")

	wt := env.addWorktree("feature-on-kroc")
	env.commitFile(wt, "feature.txt", "new feature", "add feature on krocodile")

	out := env.wt("diff", "feature-on-kroc")
	t.Log("output:\n" + out)

	assertContains(t, out, "feature.txt")
	if strings.Contains(out, "kroc.txt") {
		t.Error("diff should be against origin/krocodile (upstream), not origin/main; kroc.txt should not appear")
	}
}

func TestLs_NonDefaultBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	gitCmd(t, env.repo, "checkout", "-b", "krocodile")
	env.commitFile(env.repo, "kroc.txt", "krocodile content", "krocodile base")
	gitCmd(t, env.repo, "push", "origin", "krocodile")

	wt := env.addWorktree("kroc-merged")
	env.commitFile(wt, "f.txt", "done", "feature")
	gitCmd(t, env.repo, "push", "origin", "kroc-merged")
	gitCmd(t, env.repo, "checkout", "krocodile")
	gitCmd(t, env.repo, "merge", "--no-ff", "kroc-merged", "-m", "merge kroc-merged")
	gitCmd(t, env.repo, "push", "origin", "krocodile")
	gitCmd(t, env.repo, "fetch", "origin")

	env.simulateSession(wt)

	out := env.wt("ls")
	t.Log("output:\n" + out)

	if !strings.Contains(out, "kroc-merged") {
		t.Error("worktree should appear in ls output")
	}
	if strings.Contains(out, "committed") {
		t.Error("worktree should not be classified as committed; root branch derivation is wrong")
	}
}

// --- Child layout tests ---

func TestLs_ChildLayout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

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

	env.addChildWorktree("child-empty")

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

// --- Create / resume tests ---

func TestHelp(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	cmd := exec.Command(wtBinary, "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wt --help failed: %v\n%s", err, out)
	}
	assertContains(t, string(out), "worktree manager")
}

func TestCreate_NewWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	out := env.wt(".")
	t.Log("output:\n" + out)

	repoBase := filepath.Base(env.repo)
	nameRe := regexp.MustCompile(regexp.QuoteMeta(repoBase) + `-[0-9a-f]{7}`)
	match := nameRe.FindString(out)
	if match == "" {
		t.Fatalf("output does not contain a worktree name matching %s-<7hex>:\n%s", repoBase, out)
	}

	wtDir := filepath.Join(filepath.Dir(env.repo), match)
	if _, err := os.Stat(wtDir); os.IsNotExist(err) {
		t.Errorf("worktree directory %s was not created", wtDir)
	}

	wtList := gitCmd(t, env.repo, "worktree", "list")
	assertContains(t, wtList, match)

	branchList := gitCmd(t, env.repo, "branch", "--list", match)
	if strings.TrimSpace(branchList) == "" {
		t.Errorf("branch %s was not created", match)
	}
}

func TestCreate_RunsOpenCode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	out := env.wt(".")
	t.Log("output:\n" + out)

	// The mock opencode creates .opencode/ directory
	repoBase := filepath.Base(env.repo)
	nameRe := regexp.MustCompile(regexp.QuoteMeta(repoBase) + `-[0-9a-f]{7}`)
	match := nameRe.FindString(out)
	if match == "" {
		t.Fatalf("output does not contain a worktree name:\n%s", out)
	}

	wtDir := filepath.Join(filepath.Dir(env.repo), match)
	ocDir := filepath.Join(wtDir, ".opencode")
	if _, err := os.Stat(ocDir); os.IsNotExist(err) {
		t.Error("mock opencode should have created .opencode/ directory")
	}
}

func TestResume_ExistingWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	env.addWorktree("test-resume")

	out := env.wt("test-resume")
	t.Log("output:\n" + out)

	assertContains(t, out, "test-resume")
}

func TestBareWt_ShowsHelp(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	cmd := exec.Command(wtBinary)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bare wt failed: %v\n%s", err, out)
	}
	assertContains(t, string(out), "worktree manager")
}

func TestDispatch_UnknownName(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	out, code := env.wtWithExit("nonexistent-name")
	t.Log("output:\n" + out)

	if code == 0 {
		t.Error("expected non-zero exit for unknown worktree name")
	}
	assertContains(t, out, "not found")
}

// Suppress unused import warnings
var _ = time.Now
var _ = fmt.Sprint
