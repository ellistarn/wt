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

// Row is a single row in the worktree table. Callers provide the Entry and a
// pre-formatted Status string (e.g. "working", "merged", "removed").
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
			return rj // removable entries sink to bottom
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

// PrintTable prints rows as an aligned table. Removable statuses get a * suffix.
// serverPort is the OpenCode server port used to render the URI column.
func PrintTable(rows []Row, serverPort int) {
	if len(rows) == 0 {
		return
	}
	if cmdlog.HasLogged() {
		fmt.Println()
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintf(w, "WORKTREE\tSTATUS\tTITLE\tURI\tTOKENS\tACTIVITY\tAGE\n")

	now := time.Now()
	for _, r := range rows {
		e := r.Entry
		status := r.Status
		if removableStatuses[status] {
			status += " *"
		}
		activity := formatActivity(e.UpdatedAt, now)
		tokens := formatTokens(e.Tokens)
		title := e.Title
		if title == "" {
			title = "-"
		}
		uri := formatURI(e.Host, e.Dir, serverPort)
		age := formatDuration(e.CreatedAt, now)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, status, title, uri, tokens, activity, age)
	}

	w.Flush()
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

// formatRepo shortens the repo path by replacing $HOME with ~ and eliding
// middle segments when the path is long. Short paths are left as-is.
func formatRepo(repo string) string {
	// Replace home prefix with ~
	if home, err := os.UserHomeDir(); err == nil {
		if repo == home {
			return "~"
		}
		if strings.HasPrefix(repo, home+"/") {
			repo = "~/" + strings.TrimPrefix(repo, home+"/")
		}
	}
	parts := strings.Split(repo, "/")
	// Only elide if there are enough segments to actually shorten.
	// ~/a/b/c (4 parts) → no elision needed
	// ~/a/b/c/d/e (6 parts) → ~/…/c/d/e
	if len(parts) > 5 {
		tail := strings.Join(parts[len(parts)-3:], "/")
		return "~/.../" + tail
	}
	return repo
}

// formatURI combines host and worktree dir into a single host:port/path string.
func formatURI(host, repo string, port int) string {
	if host == "" {
		host = "localhost"
	}
	path := formatRepo(repo)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return fmt.Sprintf("%s:%d%s", host, port, path)
}
