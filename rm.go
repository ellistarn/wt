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

// classifyAll classifies all entries, batching remote entries into a single
// SSH call per host and classifying local entries in parallel goroutines.
func classifyAll(all []worktree.Entry, pulled pullResult) []string {
	statuses := make([]string, len(all))

	type remoteEntry struct {
		idx   int
		entry worktree.Entry
	}
	remoteByHost := make(map[string][]remoteEntry)
	var localIdxs []int
	for i, e := range all {
		if e.Host != "" {
			remoteByHost[e.Host] = append(remoteByHost[e.Host], remoteEntry{i, e})
		} else {
			localIdxs = append(localIdxs, i)
		}
	}

	var wg sync.WaitGroup

	// Remote: wait for pulls, then one SSH call per host.
	for host, entries := range remoteByHost {
		wg.Add(1)
		go func(host string, entries []remoteEntry) {
			defer wg.Done()
			seen := make(map[string]bool)
			for _, re := range entries {
				if !seen[re.entry.Repo] {
					seen[re.entry.Repo] = true
					pulled.Wait(re.entry)
				}
			}
			var batchEntries []git.ClassifyEntry
			var batchIdxs []int
			for _, re := range entries {
				e := re.entry
				if e.Attached {
					statuses[re.idx] = "attached"
					continue
				}
				if e.Status == "working" {
					statuses[re.idx] = "working"
					continue
				}
				batchEntries = append(batchEntries, git.ClassifyEntry{
					Dir:    e.Dir,
					Repo:   e.Repo,
					Branch: e.Name,
				})
				batchIdxs = append(batchIdxs, re.idx)
			}
			results, err := git.ClassifyBatch(host, batchEntries)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
				return
			}
			for j, r := range results {
				idx := batchIdxs[j]
				statuses[idx] = classifyFromResult(all[idx], r.Clean, r.Unique, r.Merged, r.Behind)
			}
		}(host, entries)
	}

	// Local: parallel goroutines.
	sem := make(chan struct{}, 8)
	for _, i := range localIdxs {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, entry worktree.Entry) {
			defer wg.Done()
			defer func() { <-sem }()
			pulled.Wait(entry)
			statuses[idx] = classifyStatus(entry)
		}(i, all[i])
	}

	wg.Wait()
	return statuses
}

// classifyStatus returns the single highest-priority status for a worktree.
// Priority: attached > working > dirty > merged/committed > empty > merged(behind) > stale > idle.
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

	// Merged with zero unique commits: the branch's commits are reachable
	// from upstream (regular merge commit, fast-forward, or local rebase).
	// rev-list sees zero unique commits because the branch hasn't diverged
	// from upstream's ancestry graph. Detect by checking if the branch is
	// a proper ancestor of upstream (behind, not at the same commit).
	// Only checked when a session exists — a worktree with no session
	// never had work to merge.
	if git.IsBehindUpstream(host, e.Repo, e.Name) {
		return "merged"
	}

	if !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > opencode.StaleThreshold {
		return "stale"
	}
	return "idle"
}

// classifyFromResult classifies a worktree using pre-computed git results
// (from a batch SSH call) instead of making individual git calls.
// Unlike classifyStatus, this does NOT check Attached or working status —
// callers must handle those cases before calling this function.
func classifyFromResult(e worktree.Entry, clean bool, unique int, merged bool, behind bool) string {
	if !clean {
		return "dirty"
	}
	if unique > 0 {
		if merged {
			return "merged"
		}
		return "committed"
	}
	if e.SessionID == "" {
		return "empty"
	}
	if behind {
		return "merged"
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

func cmdRmBatch() {
	all, pulled, enrichErr := discoverAll(true)
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

	statuses := classifyAll(all, pulled)

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
	display.PrintTable(rows, opencode.ServerPort())

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
	}}, opencode.ServerPort())
}
