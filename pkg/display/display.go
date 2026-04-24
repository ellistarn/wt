package display

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ellistarn/wt/pkg/worktree"
)

// PrintTable prints worktree entries as an aligned table.
func PrintTable(entries []worktree.Entry) {
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)

	fmt.Fprintf(w, "WORKTREE\tTITLE\tSTATUS\tACTIVITY\tTOKENS\tREPO\tAGE\n")

	now := time.Now()
	for _, e := range entries {
		status := formatStatus(e.Status, e.Attached)
		activity := formatActivity(e.UpdatedAt, now)
		tokens := formatTokens(e.Tokens)
		title := e.Title
		if title == "" {
			title = "-"
		}
		repo := formatRepo(e.Repo, e.Remote)
		age := formatDuration(e.CreatedAt, now)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, title, status, activity, tokens, repo, age)
	}

	w.Flush()
}

// formatStatus returns the highest-priority state: attached > working > idle > -.
func formatStatus(status string, attached bool) string {
	if status == "" {
		return "-"
	}
	if attached {
		return "attached"
	}
	return status
}

// formatActivity returns how long ago the session was active, or "now" if streaming.
func formatActivity(updatedAt time.Time, now time.Time) string {
	if updatedAt.IsZero() {
		return "-"
	}
	return formatDuration(updatedAt, now)
}

// formatDuration returns a compact relative time string.
func formatDuration(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// formatTokens formats a token count as a compact string (e.g. "12k", "1.5M").
func formatTokens(tokens int) string {
	if tokens == 0 {
		return "-"
	}
	switch {
	case tokens < 1000:
		return fmt.Sprintf("%d", tokens)
	case tokens < 1_000_000:
		k := float64(tokens) / 1000
		if k < 10 {
			return fmt.Sprintf("%.1fk", k)
		}
		return fmt.Sprintf("%dk", int(k))
	default:
		m := float64(tokens) / 1_000_000
		if m < 10 {
			return fmt.Sprintf("%.1fM", m)
		}
		return fmt.Sprintf("%dM", int(m))
	}
}

// formatRepo shortens the repo path to <home>/.../last/two and tags remote entries.
func formatRepo(repo string, remote bool) string {
	parts := strings.Split(repo, "/")
	// Shorten: keep first 3 segments (e.g., "", "Users", "etarn"), then .../, then last 2.
	// Only shorten if there are more than 5 segments (home + at least 3 intermediate + 2 tail).
	if len(parts) > 5 {
		head := strings.Join(parts[:3], "/") // e.g., /Users/etarn
		tail := strings.Join(parts[len(parts)-2:], "/")
		repo = head + "/.../" + tail
	}
	if remote {
		return "[remote] " + repo
	}
	return repo
}


func FormatAge(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}
