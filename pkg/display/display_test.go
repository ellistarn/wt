package display

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ellistarn/wt/pkg/worktree"
)

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		tokens int
		want   string
	}{
		{0, "-"},
		{500, "500"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{9999, "10.0k"},
		{10000, "10k"},
		{42000, "42k"},
		{150000, "150k"},
		{999999, "999k"},
		{1000000, "1.0M"},
		{1500000, "1.5M"},
		{10000000, "10M"},
		{25000000, "25M"},
	}
	for _, tt := range tests {
		got := formatTokens(tt.tokens)
		if got != tt.want {
			t.Errorf("formatTokens(%d) = %q, want %q", tt.tokens, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	now := time.Now()
	tests := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "-"},
		{now.Add(-30 * time.Second), "now"},
		{now.Add(-5 * time.Minute), "5m"},
		{now.Add(-3 * time.Hour), "3h"},
		{now.Add(-24 * time.Hour), "1d"},
		{now.Add(-72 * time.Hour), "3d"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.t, now)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.t, got, tt.want)
		}
	}
}

func TestFormatRepo(t *testing.T) {
	tests := []struct {
		repo string
		want string
	}{
		{"/home/user/src/github.com/acme/project", "~/.../github.com/acme/project"},
		{"/short/path", "/short/path"},
	}
	for _, tt := range tests {
		got := formatRepo(tt.repo)
		if got != tt.want {
			t.Errorf("formatRepo(%q) = %q, want %q", tt.repo, got, tt.want)
		}
	}
}

func TestFormatURI(t *testing.T) {
	tests := []struct {
		host string
		repo string
		want string
	}{
		// Local worktree (empty host) gets localhost.
		{"", "/home/user/src/github.com/acme/project", "localhost:5096~/.../github.com/acme/project"},
		// Remote worktree keeps the provided host.
		{"devbox", "/home/user/src/github.com/acme/project", "devbox:5096~/.../github.com/acme/project"},
		// Short repo path passes through unshortened.
		{"", "/short/path", "localhost:5096/short/path"},
	}
	for _, tt := range tests {
		got := formatURI(tt.host, tt.repo)
		if got != tt.want {
			t.Errorf("formatURI(%q, %q) = %q, want %q", tt.host, tt.repo, got, tt.want)
		}
	}
}

func TestFormatURICustomPort(t *testing.T) {
	t.Setenv("WT_OPENCODE_PORT", "9999")
	got := formatURI("", "/short/path")
	want := "localhost:9999/short/path"
	if got != want {
		t.Errorf("formatURI with WT_OPENCODE_PORT=9999: got %q, want %q", got, want)
	}
}

func TestPrintTable(t *testing.T) {
	now := time.Now()
	rows := []Row{
		{
			Entry: worktree.Entry{
				Name:      "a3f8c12",
				Dir:       "/home/user/src/github.com/acme/project/.worktrees/a3f8c12",
				Repo:      "/home/user/src/github.com/acme/project",
				Status:    "idle",
				Title:     "Fix auth handler",
				Tokens:    42000,
				CreatedAt: now.Add(-3 * time.Hour),
				UpdatedAt: now.Add(-5 * time.Minute),
			},
			Status: "idle",
		},
		{
			Entry: worktree.Entry{
				Name:      "b7e2a09",
				Dir:       "/home/user/src/github.com/acme/project/.worktrees/b7e2a09",
				Repo:      "/home/user/src/github.com/acme/project",
				CreatedAt: now.Add(-1 * time.Hour),
			},
			Status: "empty",
		},
	}

	// Capture stdout by swapping os.Stdout with a pipe.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w

	PrintTable(rows)

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (header + 2 rows), got %d:\n%s", len(lines), output)
	}

	// Header should have the right columns.
	header := lines[0]
	for _, col := range []string{"WORKTREE", "STATUS", "TITLE", "URI", "TOKENS", "ACTIVITY", "AGE"} {
		if !strings.Contains(header, col) {
			t.Errorf("header missing column %q: %s", col, header)
		}
	}

	// First row: idle session with activity and tokens.
	if !strings.Contains(lines[1], "idle") {
		t.Errorf("expected 'idle' in row 1: %s", lines[1])
	}
	if !strings.Contains(lines[1], "5m") {
		t.Errorf("expected '5m' activity in row 1: %s", lines[1])
	}
	if !strings.Contains(lines[1], "42k") {
		t.Errorf("expected '42k' tokens in row 1: %s", lines[1])
	}
	if !strings.Contains(lines[1], "Fix auth handler") {
		t.Errorf("expected title in row 1: %s", lines[1])
	}

	// Second row: empty status with * marker.
	if !strings.Contains(lines[2], "b7e2a09") {
		t.Errorf("expected worktree name in row 2: %s", lines[2])
	}
	if !strings.Contains(lines[2], "empty *") {
		t.Errorf("expected 'empty *' in row 2: %s", lines[2])
	}
}
