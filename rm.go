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
	"github.com/ellistarn/wt/pkg/worktree"
)

// cmdRm handles: wt rm [name]
func cmdRm(args []string) {
	if len(args) > 1 {
		die("unexpected argument: %s", args[1])
	}
	if len(args) == 1 {
		cmdRmTargeted(args[0])
	} else {
		cmdRmBatch()
	}
}

// classifyAll classifies all entries in parallel.
func classifyAll(all []worktree.Entry, pulled pullResult) []string {
	statuses := make([]string, len(all))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	for i, e := range all {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, entry worktree.Entry) {
			defer wg.Done()
			defer func() { <-sem }()
			pulled.Wait(entry)
			statuses[idx] = classifyStatus(entry)
		}(i, e)
	}
	wg.Wait()
	return statuses
}

// classifyStatus returns the single highest-priority status for a worktree.
func classifyStatus(e worktree.Entry) string {
	if e.Status == "active" {
		return "active"
	}

	hasDiff := git.HasDiff(e.Dir, e.Repo)

	if !hasDiff {
		if e.Branch == "" {
			if !e.HasSession() {
				return "empty"
			}
			if !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > worktree.StaleThreshold {
				return "stale"
			}
			return "idle"
		}
		unique := git.UniqueCommitCount(e.Repo, e.Branch)
		if unique > 0 && git.IsMerged(e.Repo, e.Branch) {
			return "merged"
		}
		if !e.HasSession() {
			return "empty"
		}
		if unique == 0 && git.IsBehindUpstream(e.Repo, e.Branch) {
			return "merged"
		}
		if !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > worktree.StaleThreshold {
			return "stale"
		}
		return "idle"
	}

	if e.Branch == "" {
		return "dirty"
	}
	unique := git.UniqueCommitCount(e.Repo, e.Branch)
	if unique == 0 {
		return "dirty"
	}
	if git.IsMerged(e.Repo, e.Branch) {
		if git.HasUncommittedChanges(e.Dir) {
			return "dirty"
		}
		return "merged"
	}
	return "committed"
}

func isRemovable(status string) bool {
	return status == "merged" || status == "stale" || status == "empty"
}

type rmResult struct {
	entry  worktree.Entry
	status string
	errMsg string
}

func sortRmResults(results []rmResult) {
	sort.SliceStable(results, func(i, j int) bool {
		ri, rj := results[i].status == "removed", results[j].status == "removed"
		if ri != rj {
			return rj
		}
		ti, tj := results[i].entry.UpdatedAt, results[j].entry.UpdatedAt
		if !ti.IsZero() && !tj.IsZero() {
			return ti.After(tj)
		}
		if !ti.IsZero() {
			return true
		}
		return false
	})
}

func cmdRmBatch() {
	all, pulled := discoverAll(true)

	if len(all) == 0 {
		fmt.Println("No worktrees found.")
		return
	}

	statuses := classifyAll(all, pulled)

	var results []rmResult
	var removeCount int

	for i, e := range all {
		status := statuses[i]
		var errMsg string

		if isRemovable(status) {
			if err := git.WorktreeRemove(e.Repo, e.Branch, e.Dir); err != nil {
				errMsg = strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " ")
			} else {
				status = "removed"
				removeCount++
			}
		}

		results = append(results, rmResult{e, status, errMsg})
	}

	sortRmResults(results)

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
	if err := git.WorktreeForceRemove(entry.Repo, entry.Branch, entry.Dir); err != nil {
		die("%v", err)
	}
	display.PrintTable([]display.Row{{
		Entry:  entry,
		Status: "removed",
	}})
}
