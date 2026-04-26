package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ellistarn/wt/pkg/display"
	"github.com/ellistarn/wt/pkg/git"
	"github.com/ellistarn/wt/pkg/opencode"
	"github.com/ellistarn/wt/pkg/worktree"
)

// cmdRm handles: wt rm [name]
func cmdRm(args []string, remoteOnly bool) {
	if len(args) > 1 {
		die("unexpected argument: %s", args[1])
	}
	if len(args) == 1 {
		cmdRmTargeted(args[0])
	} else {
		cmdRmBatch(remoteOnly)
	}
}

// classifyStatus returns the single highest-priority status for a worktree.
// Priority: attached > working > dirty > merged > committed > idle > stale > empty.
func classifyStatus(e worktree.Entry) string {
	// Session states — active use takes priority
	if e.Attached {
		return "attached"
	}
	if e.Status == "working" {
		return "working"
	}

	// Git states — data safety
	host := hostFor(e)
	if !git.IsClean(host, e.Dir) {
		return "dirty"
	}
	unique := git.UniqueCommitCount(host, e.Repo, e.Name)
	if unique > 0 {
		if git.IsMerged(host, e.Repo, e.Name) {
			return "merged"
		}
		return "committed"
	}

	// Session lifecycle — no unique commits, clean tree
	if e.SessionID == "" {
		return "empty"
	}
	if !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > opencode.StaleThreshold {
		return "stale"
	}
	return "idle"
}

// isRemovable returns true if a status indicates the worktree is safe to remove.
func isRemovable(status string) bool {
	return status == "merged" || status == "stale" || status == "empty"
}

func cmdRmBatch(remoteOnly bool) {
	all, enrichErr := discoverAll(remoteOnly)
	if enrichErr != nil {
		die("cannot determine session status: %v", enrichErr)
	}

	if len(all) == 0 {
		fmt.Println("No worktrees found.")
		return
	}

	type result struct {
		entry  worktree.Entry
		status string
		errMsg string
	}

	// Classify in parallel
	statuses := make([]string, len(all))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, e := range all {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, entry worktree.Entry) {
			defer wg.Done()
			defer func() { <-sem }()
			statuses[idx] = classifyStatus(entry)
		}(i, e)
	}
	wg.Wait()

	// Remove sequentially
	var results []result
	var removeCount int

	for i, e := range all {
		status := statuses[i]
		var errMsg string

		if isRemovable(status) {
			host := hostFor(e)
			if err := git.WorktreeRemove(host, e.Repo, e.Name); err != nil {
				errMsg = strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " ")
			} else {
				status = "removed"
				removeCount++
			}
		}

		results = append(results, result{e, status, errMsg})
	}

	// Sort: removed first, then by activity (newest first)
	sort.SliceStable(results, func(i, j int) bool {
		ri, rj := results[i].status == "removed", results[j].status == "removed"
		if ri != rj {
			return ri
		}
		ti, tj := results[i].entry.UpdatedAt, results[j].entry.UpdatedAt
		if !ti.IsZero() && !tj.IsZero() {
			return ti.After(tj)
		}
		if !ti.IsZero() {
			return true
		}
		return !tj.IsZero() && false
	})

	rows := make([]display.Row, len(results))
	for i, r := range results {
		rows[i] = display.Row{
			Entry:  r.entry,
			Status: r.status,
		}
	}
	display.PrintTable(rows)

	for _, r := range results {
		if r.errMsg != "" {
			fmt.Fprintf(os.Stderr, "ERROR: %s: %s\n", r.entry.Name, r.errMsg)
		}
	}

	if removeCount == 0 {
		fmt.Println()
		fmt.Println("Nothing to remove. Use 'wt rm <name>' to target specific worktrees.")
	}
}

func cmdRmTargeted(name string) {
	entry, ok := findWorktree(name)
	if !ok {
		die("worktree %q not found", name)
	}
	host := hostFor(entry)
	if err := git.WorktreeForceRemove(host, entry.Repo, entry.Name); err != nil {
		die("%v", err)
	}
	display.PrintTable([]display.Row{{
		Entry:  entry,
		Status: "removed",
	}})
}
