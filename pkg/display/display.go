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

	fmt.Fprintf(w, "WORKTREE\tSTATUS\tTITLE\tAGE\tACTIVITY\tREPO\n")

	now := time.Now()
	for _, e := range entries {
		status := e.Status
		if status == "" {
			status = "-"
		}
		title := e.Title
		if title == "" {
			title = "-"
		}
		age := formatAge(e.CreatedAt, now)
		activity := formatAge(e.UpdatedAt, now)
		repo := formatRepo(e.Repo, e.Remote)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, status, title, age, activity, repo)
	}

	w.Flush()
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

func formatAge(t time.Time, now time.Time) string {
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
