package opencode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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

// TestFetchSessionTokens_RegressionContextWindow verifies that fetchSessionTokens
// returns the last assistant message's total (context window size), not the sum
// across all messages. Summing double-counts input context that is re-sent each turn.
func TestFetchSessionTokens_RegressionContextWindow(t *testing.T) {
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
		}{Total: 25000}}},
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
		}{Total: 50000}}},
		// Trailing zero-total message (incomplete/streaming).
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
		}{Total: 0}}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(messages)
	}))
	defer srv.Close()

	got := fetchSessionTokens(srv.URL, "test-session")

	// Must return 50000 (last non-zero assistant total), not 75000 (sum).
	if got != 50000 {
		t.Errorf("fetchSessionTokens = %d, want 50000 (context window size, not sum)", got)
	}
}
