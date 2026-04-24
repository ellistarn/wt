package opencode

import (
	"strings"
	"testing"
	"time"

	"github.com/ellistarn/wt/pkg/worktree"
)

// skipIfNoServer skips the test if the local OpenCode server is not reachable.
func skipIfNoServer(t *testing.T) string {
	t.Helper()
	serverURL := LocalServerURL()
	if err := CheckHealth(serverURL); err != nil {
		t.Skipf("local opencode server not running at %s", serverURL)
	}
	return serverURL
}

// findSessionDir returns a directory that has at least one session on the server.
func findSessionDir(t *testing.T, serverURL string) string {
	t.Helper()
	sessions := fetchSessions(serverURL, "")
	for _, s := range sessions {
		if s.Directory != "" {
			return s.Directory
		}
	}
	t.Skip("no sessions with a directory on the server")
	return ""
}

func TestHealthCheck(t *testing.T) {
	serverURL := skipIfNoServer(t)
	if err := CheckHealth(serverURL); err != nil {
		t.Fatalf("expected healthy server at %s: %v", serverURL, err)
	}
}

func TestHealthCheck_Unreachable(t *testing.T) {
	if err := CheckHealth("http://localhost:1"); err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestFindLatestSession(t *testing.T) {
	serverURL := skipIfNoServer(t)
	dir := findSessionDir(t, serverURL)

	sessionID := FindLatestSession(serverURL, dir)
	if sessionID == "" {
		t.Fatalf("no session found for %s", dir)
	}
	t.Logf("directory=%s session=%s", dir, sessionID)
}

func TestFindLatestSession_UnknownDir(t *testing.T) {
	serverURL := skipIfNoServer(t)

	sessionID := FindLatestSession(serverURL, "/nonexistent/path/xyz")
	if sessionID != "" {
		t.Fatalf("expected empty, got %s", sessionID)
	}
}

func TestEnrich(t *testing.T) {
	serverURL := skipIfNoServer(t)
	dir := findSessionDir(t, serverURL)

	entries := []worktree.Entry{
		{
			Name:      "known-worktree",
			Dir:       dir,
			Repo:      dir,
			CreatedAt: time.Now().Add(-1 * time.Hour),
		},
		{
			Name: "nonexistent-worktree",
			Dir:  "/nonexistent/path/xyz",
			Repo: "/nonexistent",
		},
	}

	Enrich(serverURL, entries)

	// First entry should be enriched.
	e := entries[0]
	if e.SessionID == "" {
		t.Error("expected SessionID to be populated")
	}
	if e.Title == "" {
		t.Error("expected Title to be populated")
	}
	if e.Status == "" {
		t.Error("expected Status to be populated")
	}
	if e.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be populated")
	}
	if e.Tokens == 0 {
		t.Error("expected Tokens > 0")
	}
	t.Logf("enriched: session=%s title=%q status=%s tokens=%d", e.SessionID, e.Title, e.Status, e.Tokens)

	// Second entry should remain empty.
	e2 := entries[1]
	if e2.SessionID != "" {
		t.Errorf("expected empty SessionID for unknown dir, got %s", e2.SessionID)
	}
	if e2.Tokens != 0 {
		t.Errorf("expected zero tokens for unknown dir, got %d", e2.Tokens)
	}
}

func TestEnrich_Empty(t *testing.T) {
	serverURL := skipIfNoServer(t)
	Enrich(serverURL, nil)
	Enrich(serverURL, []worktree.Entry{})
}

// Bug 3: Enrich against an unreachable server should return quickly,
// not hang for N * 5s (one timeout per entry).
func TestEnrich_UnreachableServer(t *testing.T) {
	entries := []worktree.Entry{
		{Name: "a", Dir: "/fake/dir/a"},
		{Name: "b", Dir: "/fake/dir/b"},
		{Name: "c", Dir: "/fake/dir/c"},
	}

	// Use a non-routable IP to trigger actual TCP timeouts (not instant connection refused).
	start := time.Now()
	Enrich("http://192.0.2.1:9999", entries)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("Enrich against unreachable server took %v, expected < 3s", elapsed)
	}

	// Entries should remain untouched.
	for _, e := range entries {
		if e.SessionID != "" {
			t.Errorf("expected empty SessionID, got %s", e.SessionID)
		}
	}
}

func TestTokenCounting(t *testing.T) {
	serverURL := skipIfNoServer(t)

	sessions := fetchSessions(serverURL, "")
	if len(sessions) == 0 {
		t.Skip("no sessions on server")
	}

	// Find a session with tokens.
	for _, s := range sessions {
		m := fetchSessionMetrics(serverURL, s.ID)
		if m.tokens > 0 {
			t.Logf("session %s (%q): %d tokens, status=%s", s.ID, s.Title, m.tokens, m.status)
			return
		}
	}
	t.Fatal("no sessions with tokens found")
}

// Bug 7: Status must be derived from message data, not the project-scoped
// /session/status endpoint. Verify that Enrich populates status for
// cross-project sessions.
func TestEnrich_CrossProjectStatus(t *testing.T) {
	serverURL := skipIfNoServer(t)

	// Use a worktree directory that's in a different project than the server's.
	sessions := fetchSessions(serverURL, "")
	var crossDir string
	for _, s := range sessions {
		if strings.Contains(s.Directory, ".worktrees") {
			crossDir = s.Directory
			break
		}
	}
	if crossDir == "" {
		t.Skip("no cross-project sessions found")
	}

	entries := []worktree.Entry{
		{Name: "cross", Dir: crossDir, Repo: crossDir, CreatedAt: time.Now()},
	}
	Enrich(serverURL, entries)

	if entries[0].Status == "" {
		t.Error("expected Status to be populated for cross-project session")
	}
	t.Logf("cross-project status=%s tokens=%d", entries[0].Status, entries[0].Tokens)
}

func TestAttachedDirs(t *testing.T) {
	dirs := AttachedDirs()
	// We can't predict exact results, but the function should not panic
	// and should return a non-nil map.
	if dirs == nil {
		t.Fatal("AttachedDirs returned nil")
	}
	t.Logf("attached dirs: %v", dirs)

	// If there are any opencode attach processes running, verify they
	// have real directory paths.
	for dir, count := range dirs {
		if dir == "" {
			t.Error("empty directory in AttachedDirs result")
		}
		if count < 1 {
			t.Errorf("expected count >= 1 for %s, got %d", dir, count)
		}
	}
}
