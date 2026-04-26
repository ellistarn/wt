package opencode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ellistarn/wt/pkg/worktree"
)

func TestHealthCheck(t *testing.T) {
	serverURL := LocalServerURL()
	if err := CheckHealth(serverURL); err != nil {
		t.Skipf("local opencode server not running at %s", serverURL)
	}
}

func TestHealthCheck_Unreachable(t *testing.T) {
	if err := CheckHealth("http://localhost:1"); err == nil {
		t.Fatal("expected error for unreachable server")
	}
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
	for dir := range dirs {
		if dir == "" {
			t.Error("empty directory in AttachedDirs result")
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
			line:    "/usr/local/bin/node /usr/local/bin/opencode attach http://localhost:5096 --dir /home/me/.worktrees/fix-auth",
			wantDir: "/home/me/.worktrees/fix-auth",
		},
		{
			name:    "native binary",
			line:    "/usr/lib/opencode/bin/opencode attach http://localhost:5096 --dir /home/me/.worktrees/fix-auth",
			wantDir: "/home/me/.worktrees/fix-auth",
		},
		{
			name: "bare opencode ignored",
			line: "/usr/local/bin/opencode",
		},
		{
			name: "server process ignored",
			line: "/usr/local/bin/opencode serve --port 5096",
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
			line: "/usr/local/bin/opencode attach http://localhost:5096",
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

// TestFetchSessionStatus_ContextWindow verifies that fetchSessionStatus
// returns the last assistant message's total (context window size), not the sum
// across all messages. Summing double-counts input context that is re-sent each turn.
// Also verifies that a trailing zero-total, zero-completed message is detected as streaming.
func TestFetchSessionStatus_ContextWindow(t *testing.T) {
	messages := []message{
		{Info: struct {
			Role   string `json:"role"`
			Tokens struct {
				Total int `json:"total"`
			} `json:"tokens"`
			Time struct {
				Completed int64 `json:"completed"`
			} `json:"time"`
		}{Role: "user"}},
		{Info: struct {
			Role   string `json:"role"`
			Tokens struct {
				Total int `json:"total"`
			} `json:"tokens"`
			Time struct {
				Completed int64 `json:"completed"`
			} `json:"time"`
		}{Role: "assistant", Tokens: struct {
			Total int `json:"total"`
		}{Total: 25000}, Time: struct {
			Completed int64 `json:"completed"`
		}{Completed: 1700000000000}}},
		{Info: struct {
			Role   string `json:"role"`
			Tokens struct {
				Total int `json:"total"`
			} `json:"tokens"`
			Time struct {
				Completed int64 `json:"completed"`
			} `json:"time"`
		}{Role: "user"}},
		{Info: struct {
			Role   string `json:"role"`
			Tokens struct {
				Total int `json:"total"`
			} `json:"tokens"`
			Time struct {
				Completed int64 `json:"completed"`
			} `json:"time"`
		}{Role: "assistant", Tokens: struct {
			Total int `json:"total"`
		}{Total: 50000}, Time: struct {
			Completed int64 `json:"completed"`
		}{Completed: 1700000001000}}},
		// Trailing zero-total message (incomplete/streaming): completed == 0.
		{Info: struct {
			Role   string `json:"role"`
			Tokens struct {
				Total int `json:"total"`
			} `json:"tokens"`
			Time struct {
				Completed int64 `json:"completed"`
			} `json:"time"`
		}{Role: "assistant", Tokens: struct {
			Total int `json:"total"`
		}{Total: 0}, Time: struct {
			Completed int64 `json:"completed"`
		}{Completed: 0}}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(messages)
	}))
	defer srv.Close()

	got := fetchSessionStatus(srv.URL, "test-session")

	// Must return 50000 (last non-zero assistant total), not 75000 (sum).
	if got.tokens != 50000 {
		t.Errorf("tokens = %d, want 50000 (context window size, not sum)", got.tokens)
	}
	// Trailing assistant message has completed == 0, so streaming should be true.
	if !got.streaming {
		t.Error("streaming = false, want true (last assistant message has completed == 0)")
	}
}

// TestFetchSessionStatus_Idle verifies that a completed message returns streaming=false.
func TestFetchSessionStatus_Idle(t *testing.T) {
	messages := []message{
		{Info: struct {
			Role   string `json:"role"`
			Tokens struct {
				Total int `json:"total"`
			} `json:"tokens"`
			Time struct {
				Completed int64 `json:"completed"`
			} `json:"time"`
		}{Role: "user"}},
		{Info: struct {
			Role   string `json:"role"`
			Tokens struct {
				Total int `json:"total"`
			} `json:"tokens"`
			Time struct {
				Completed int64 `json:"completed"`
			} `json:"time"`
		}{Role: "assistant", Tokens: struct {
			Total int `json:"total"`
		}{Total: 30000}, Time: struct {
			Completed int64 `json:"completed"`
		}{Completed: 1700000000000}}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(messages)
	}))
	defer srv.Close()

	got := fetchSessionStatus(srv.URL, "test-session")

	if got.tokens != 30000 {
		t.Errorf("tokens = %d, want 30000", got.tokens)
	}
	if got.streaming {
		t.Error("streaming = true, want false (last assistant message has non-zero completed)")
	}
}

// TestFetchSessionStatus_NoMessages verifies that an empty message list
// returns streaming=false and tokens=0.
func TestFetchSessionStatus_NoMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]message{})
	}))
	defer srv.Close()

	got := fetchSessionStatus(srv.URL, "test-session")

	if got.tokens != 0 {
		t.Errorf("tokens = %d, want 0", got.tokens)
	}
	if got.streaming {
		t.Error("streaming = true, want false (no messages)")
	}
}

func TestWorktreeName(t *testing.T) {
	tests := []struct {
		dir  string
		want string
	}{
		{"/home/user/repo/.worktrees/0425T1457-57407", "0425T1457-57407"},
		{"/local/home/user/repo/.worktrees/0425T1457-57407", "0425T1457-57407"},
		{"/home/user/repo", ""},
		{"", ""},
		{"/home/user/.worktrees/", ""},
		{"/home/user/.worktrees/name/subdir", ""},
	}
	for _, tt := range tests {
		if got := worktreeName(tt.dir); got != tt.want {
			t.Errorf("worktreeName(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

// TestEnrich_RegressionSymlinkPaths verifies that session enrichment matches
// by worktree name, not full path. This broke when remote hosts had symlinked
// home directories (e.g. /local/home/user vs /home/user) causing the session
// directory to differ from the discovered worktree directory.
func TestEnrich_RegressionSymlinkPaths(t *testing.T) {
	sessions := []Session{
		{
			ID:        "sess-1",
			Directory: "/local/home/user/repo/.worktrees/0425T1457-57407",
			Title:     "fix auth bug",
			Time: struct {
				Created int64 `json:"created"`
				Updated int64 `json:"updated"`
			}{Updated: time.Now().UnixMilli()},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/global/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(sessions)
	})
	mux.HandleFunc("/session/sess-1/message", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]message{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	entries := []worktree.Entry{
		{
			Name: "0425T1457-57407",
			Dir:  "/home/user/repo/.worktrees/0425T1457-57407", // different path, same worktree name
		},
	}

	if err := Enrich(srv.URL, entries); err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if entries[0].SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", entries[0].SessionID, "sess-1")
	}
	if entries[0].Title != "fix auth bug" {
		t.Errorf("Title = %q, want %q", entries[0].Title, "fix auth bug")
	}
}
