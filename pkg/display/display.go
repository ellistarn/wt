package display

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ellistarn/wt/pkg/cmdlog"
	"github.com/ellistarn/wt/pkg/worktree"
)

// Row is a single row in the worktree table.
type Row struct {
	Entry  worktree.Entry
	Status string
}

// removableStatuses are cleaned up by `wt rm` and marked with * in listings.
var removableStatuses = map[string]bool{
	"merged": true,
	"stale":  true,
	"empty":  true,
}

// SortRows sorts rows for display: removable statuses (*) sink to the bottom,
// everything else sorts by most recent activity (UpdatedAt), newest first.
func SortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := removableStatuses[rows[i].Status], removableStatuses[rows[j].Status]
		if ri != rj {
			return rj
		}
		ai, aj := rows[i].Entry.UpdatedAt, rows[j].Entry.UpdatedAt
		if !ai.IsZero() && !aj.IsZero() {
			return ai.After(aj)
		}
		if !ai.IsZero() {
			return true
		}
		if !aj.IsZero() {
			return false
		}
		ci, cj := rows[i].Entry.CreatedAt, rows[j].Entry.CreatedAt
		if !ci.IsZero() && !cj.IsZero() {
			return ci.After(cj)
		}
		return rows[i].Entry.Name < rows[j].Entry.Name
	})
}

// PrintTable prints rows as an aligned table.
func PrintTable(rows []Row) {
	if len(rows) == 0 {
		return
	}
	if cmdlog.HasLogged() {
		fmt.Println()
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintf(w, "WORKTREE\tSTATUS\tTITLE\tDIR\tACTIVITY\tAGE\n")

	now := time.Now()
	for _, r := range rows {
		e := r.Entry
		status := r.Status
		if removableStatuses[status] {
			status += " *"
		}
		activity := formatActivity(e.UpdatedAt, now)
		dir := formatDir(e.Dir)
		age := formatDuration(e.CreatedAt, now)
		title := e.Title
		if title == "" {
			title = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, status, title, dir, activity, age)
	}

	w.Flush()
}

// formatActivity returns how long ago the session was active.
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

// formatDir shortens the directory path by replacing $HOME with ~.
func formatDir(dir string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if dir == home {
			return "~"
		}
		if strings.HasPrefix(dir, home+"/") {
			return "~/" + strings.TrimPrefix(dir, home+"/")
		}
	}
	return dir
}
