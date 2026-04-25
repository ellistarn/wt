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
	t         *testing.T
	rootDir   string
	dataDir   string
	repo      string
	mockURL   string
	sessions  []mockSession
	sessionMu sync.Mutex
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
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}

	name := fmt.Sprintf("wt-e2e-%d-%d", time.Now().UnixNano(), rand.Intn(100000))
	rootDir := filepath.Join(home, name)
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rootDir) })

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

		// Update timestamps to keep sessions "fresh" (within 30s = working)
		now := time.Now().UnixMilli()
		for i := range sessions {
			sessions[i].Time.Updated = now
		}

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
		// Handle /session/:id/message — return empty messages
		json.NewEncoder(w).Encode([]any{})
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	e.t.Cleanup(func() { srv.Close() })
	e.mockURL = "http://" + ln.Addr().String()
}

func (e *testEnv) wt(args ...string) string {
	e.t.Helper()
	cmd := exec.Command(wtBinary, args...)
	cmd.Dir = e.repo
	cmd.Env = append(os.Environ(),
		"DEV_DESKTOP_HOST=",
		"WT_LOCAL_SERVER="+e.mockURL,
	)
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "cannot remove") {
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

// --- Data safety tests ---

func TestTargetedRm_DirtyBlocked(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	wt := env.addWorktree("dirty")
	os.WriteFile(filepath.Join(wt, "f.txt"), []byte("x"), 0644)

	out := env.wt("rm", "dirty")
	assertContains(t, out, "cannot remove")
	assertContains(t, out, "uncommitted changes")
	if !env.worktreeExists("dirty") {
		t.Error("dirty worktree should NOT have been removed")
	}
}

func TestTargetedRm_UnpushedBlocked(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	wt := env.addWorktree("unpushed")
	env.commitFile(wt, "a.txt", "a", "local work")

	out := env.wt("rm", "unpushed")
	assertContains(t, out, "cannot remove")
	assertContains(t, out, "unpushed commit")
	if !env.worktreeExists("unpushed") {
		t.Error("unpushed worktree should NOT have been removed")
	}
}

func TestTargetedRm_AgentWorkingBlocked(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	wt := env.addWorktree("working")
	// A just-created session is "working" (assistant message < 60s old)
	env.createSession(wt)

	out := env.wt("rm", "working")
	assertContains(t, out, "cannot remove")
	assertContains(t, out, "agent is working")
	if !env.worktreeExists("working") {
		t.Error("worktree with working agent should NOT have been removed")
	}
}

func TestTargetedRm_ForceOverrides(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	wt := env.addWorktree("force")
	os.WriteFile(filepath.Join(wt, "f.txt"), []byte("x"), 0644)

	out := env.wt("rm", "force", "--force")
	assertContains(t, out, "force")
	assertContains(t, out, "removed (forced)")
	if env.worktreeExists("force") {
		t.Error("worktree should have been removed with --force")
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
	assertContains(t, out, "removed (")
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
	assertContains(t, out, "removed (")
	if env.worktreeExists("merged") {
		t.Error("merged worktree should have been removed")
	}
}

func TestTargetedRm_PushedNotMergedWarns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	wt := env.addWorktree("in-review")
	env.commitFile(wt, "f.txt", "wip", "work")
	env.push("in-review")
	// No session → "no session" gate fires, no warning expected
	// (workflow warning only appears when a session blocks the gate)

	out := env.wt("rm", "in-review")
	assertContains(t, out, "in-review")
	assertContains(t, out, "removed (")
	if env.worktreeExists("in-review") {
		t.Error("pushed worktree should have been removed in targeted mode")
	}
}

// --- Batch tests (dry-run only to avoid touching real worktrees) ---

func TestBatchRm_DryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)

	// Would remove: clean, no session
	env.addWorktree("batch-clean")

	// Would remove: merged
	wt2 := env.addWorktree("batch-merged")
	env.commitFile(wt2, "f.txt", "done", "feature")
	env.push("batch-merged")
	env.mergeToMain("batch-merged")
	gitCmd(t, env.repo, "checkout", "main")

	// Skipped: dirty
	wt3 := env.addWorktree("batch-dirty")
	os.WriteFile(filepath.Join(wt3, "f.txt"), []byte("x"), 0644)

	// Skipped: unpushed
	wt4 := env.addWorktree("batch-unpushed")
	env.commitFile(wt4, "a.txt", "a", "local")

	out := env.wt("rm", "--dry-run")
	t.Log("output:\n" + out)

	assertContains(t, out, "batch-clean")
	assertContains(t, out, "batch-merged")
	assertContains(t, out, "remove (")

	assertContains(t, out, "batch-dirty")
	assertContains(t, out, "keep (dirty")
	assertContains(t, out, "batch-unpushed")
	assertContains(t, out, "keep (unpushed")

	// Nothing actually removed
	for _, name := range []string{"batch-clean", "batch-merged", "batch-dirty", "batch-unpushed"} {
		if !env.worktreeExists(name) {
			t.Errorf("worktree %q removed during dry-run", name)
		}
	}
}

func TestBatchRm_SessionWorkingSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test")
	}
	t.Parallel()
	env := newTestEnv(t)
	wt := env.addWorktree("batch-working")
	env.createSession(wt)

	out := env.wt("rm", "--dry-run")
	t.Log("output:\n" + out)

	// Just-created session is "working" (< 60s) — data gate blocks
	assertContains(t, out, "batch-working")
	assertContains(t, out, "keep (working)")
}
