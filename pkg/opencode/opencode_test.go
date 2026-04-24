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

// findSessionDir returns a directory that has at least one session with
// assistant messages on the server. Prefers sessions with tokens so that
// enrichment tests can assert non-zero values.
func findSessionDir(t *testing.T, serverURL string) string {
	t.Helper()
	sessions := fetchSessions(serverURL, "")
	// First pass: prefer sessions with tokens.
	for _, s := range sessions {
		if s.Directory != "" {
			m := fetchSessionMetrics(serverURL, s.ID)
			if m.tokens > 0 {
				return s.Directory
			}
		}
	}
	// Fallback: any session with a directory.
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

func TestParseAttachDir(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantDir string
	}{
		{
			name:    "node wrapper",
			line:    "/opt/homebrew/opt/node/bin/node /opt/homebrew/bin/opencode attach http://localhost:9000 --dir /Users/me/.worktrees/fix-auth",
			wantDir: "/Users/me/.worktrees/fix-auth",
		},
		{
			name:    "native binary",
			line:    "/opt/homebrew/Cellar/opencode/1.4.10/libexec/lib/node_modules/opencode-ai/node_modules/opencode-darwin-arm64/bin/opencode attach http://localhost:9000 --dir /Users/me/.worktrees/fix-auth",
			wantDir: "/Users/me/.worktrees/fix-auth",
		},
		{
			name: "bare opencode ignored",
			line: "/opt/homebrew/bin/opencode",
		},
		{
			name: "server process ignored",
			line: "/opt/homebrew/bin/opencode serve --port 9000",
		},
		{
			name: "unrelated process",
			line: "/usr/bin/vim",
		},
		{
			name: "empty line",
			line: "",
		},
		{
			name: "attach without --dir",
			line: "/opt/homebrew/bin/opencode attach http://localhost:9000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAttachDir(tt.line)
			if got != tt.wantDir {
				t.Errorf("got %q, want %q", got, tt.wantDir)
			}
		})
	}
}

// helpers to build messageResponse values for unit tests.
func msg(role string, total int, completed *int64) messageResponse {
	var m messageResponse
	m.Info.Role = role
	if total > 0 {
		m.Info.Tokens = &struct {
			Total int `json:"total"`
		}{Total: total}
	}
	if completed != nil || role == "assistant" {
		m.Info.Time = &struct {
			Created   int64  `json:"created"`
			Completed *int64 `json:"completed,omitempty"`
		}{Created: 1000, Completed: completed}
	}
	return m
}

func ptr(v int64) *int64 { return &v }

func TestComputeMetrics(t *testing.T) {
	tests := []struct {
		name       string
		messages   []messageResponse
		wantTokens int
		wantStatus string
	}{
		{
			name:       "empty messages",
			messages:   nil,
			wantTokens: 0,
			wantStatus: "idle",
		},
		{
			name: "single completed assistant message",
			messages: []messageResponse{
				msg("user", 0, nil),
				msg("assistant", 30000, ptr(2000)),
			},
			wantTokens: 30000,
			wantStatus: "idle",
		},
		{
			name: "streaming: last message has total=0, fallback to previous",
			messages: []messageResponse{
				msg("user", 0, nil),
				msg("assistant", 50000, ptr(1000)),
				msg("user", 0, nil),
				msg("assistant", 0, nil), // streaming, no tokens yet
			},
			wantTokens: 50000,
			wantStatus: "working",
		},
		{
			name: "multiple completed messages uses last",
			messages: []messageResponse{
				msg("assistant", 10000, ptr(1000)),
				msg("user", 0, nil),
				msg("assistant", 25000, ptr(2000)),
			},
			wantTokens: 25000,
			wantStatus: "idle",
		},
		{
			name: "only user messages",
			messages: []messageResponse{
				msg("user", 0, nil),
				msg("user", 0, nil),
			},
			wantTokens: 0,
			wantStatus: "idle",
		},
		{
			name: "streaming with no prior completed messages",
			messages: []messageResponse{
				msg("user", 0, nil),
				msg("assistant", 0, nil), // streaming, first message
			},
			wantTokens: 0,
			wantStatus: "working",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := computeMetrics(tt.messages)
			if m.tokens != tt.wantTokens {
				t.Errorf("tokens: got %d, want %d", m.tokens, tt.wantTokens)
			}
			if m.status != tt.wantStatus {
				t.Errorf("status: got %q, want %q", m.status, tt.wantStatus)
			}
		})
	}
}

// Regression: idle sessions with a streaming last message must use the
// last *completed* message's activity time, not overwrite it with an
// older message while scanning backwards for tokens.
func TestComputeMetrics_RegressionIdleActivity(t *testing.T) {
	messages := []messageResponse{
		msg("assistant", 10000, ptr(1000)), // old
		msg("user", 0, nil),
		msg("assistant", 50000, ptr(5000)), // most recent completed
		msg("user", 0, nil),
		msg("assistant", 0, nil), // streaming
	}
	m := computeMetrics(messages)
	if m.status != "working" {
		t.Fatalf("status: got %q, want %q", m.status, "working")
	}
	if m.tokens != 50000 {
		t.Errorf("tokens: got %d, want 50000", m.tokens)
	}
}

func TestComputeMetrics_RegressionIdleNoOverwrite(t *testing.T) {
	messages := []messageResponse{
		msg("assistant", 10000, ptr(1000)), // old, completed at t=1000
		msg("user", 0, nil),
		msg("assistant", 0, ptr(5000)), // recent, completed at t=5000, no tokens
	}
	m := computeMetrics(messages)
	// lastActivity should be from the LAST assistant message (t=5000),
	// not overwritten by the earlier one (t=1000) during token scanning.
	want := time.UnixMilli(5000)
	if !m.lastActivity.Equal(want) {
		t.Errorf("lastActivity: got %v, want %v", m.lastActivity, want)
	}
	if m.tokens != 10000 {
		t.Errorf("tokens: got %d, want 10000", m.tokens)
	}
}
