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

// remoteEntry pairs an index into the all slice with its worktree entry.
type remoteEntry struct {
	idx   int
	entry worktree.Entry
}

// classifyAll classifies all entries, batching remote entries into a single
// SSH call per host and classifying local entries in parallel goroutines.
func classifyAll(all []worktree.Entry, pulled pullResult) []string {
	statuses := make([]string, len(all))

	// Partition entries into local vs remote-by-host.
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

	// Remote: one SSH call per host.
	for host, entries := range remoteByHost {
		wg.Add(1)
		go func(host string, entries []remoteEntry) {
			defer wg.Done()
			classifyRemoteEntries(host, entries, all, statuses, pulled)
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

// classifyRemoteEntries classifies entries on a single remote host via one SSH batch call.
// It waits for pulls to complete, short-circuits session states (attached/working),
// and runs git.ClassifyBatch for the remaining entries.
func classifyRemoteEntries(host string, entries []remoteEntry, all []worktree.Entry, statuses []string, pulled pullResult) {
	// Wait for each unique repo's pull to complete.
	seen := make(map[string]bool)
	for _, re := range entries {
		if !seen[re.entry.Repo] {
			seen[re.entry.Repo] = true
			pulled.Wait(re.entry)
		}
	}

	// Short-circuit session states and build the batch for git classification.
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
		if e.Branch == "" {
			// No branch to classify against upstream; use simple status.
			batchEntries = append(batchEntries, git.ClassifyEntry{
				Dir:  e.Dir,
				Repo: e.Repo,
			})
			batchIdxs = append(batchIdxs, re.idx)
			continue
		}
		batchEntries = append(batchEntries, git.ClassifyEntry{
			Dir:    e.Dir,
			Repo:   e.Repo,
			Branch: e.Branch,
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
		statuses[idx] = classifyFromResult(all[idx], r)
	}
}

// classifyStatus returns the single highest-priority status for a worktree.
// Priority: attached > working > dirty > committed > merged > empty > stale > idle.
// Uses HasDiff as the single gate: no diff = no dirty.
func classifyStatus(e worktree.Entry) string {
	// Session states — active use takes priority
	if e.Attached {
		return "attached"
	}
	if e.Status == "working" {
		return "working"
	}

	host := hostFor(e)
	hasDiff := git.HasDiff(host, e.Dir, e.Repo)

	if !hasDiff {
		// No visible changes — cannot be dirty.
		if e.Branch == "" {
			if e.SessionID == "" {
				return "empty"
			}
			if !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > opencode.StaleThreshold {
				return "stale"
			}
			return "idle"
		}
		unique := git.UniqueCommitCount(host, e.Repo, e.Branch)
		if unique > 0 && git.IsMerged(host, e.Repo, e.Branch) {
			return "merged"
		}
		// Session check before behind: a worktree with no session never had
		// work to merge, so "behind upstream" is irrelevant.
		if e.SessionID == "" {
			return "empty"
		}
		if unique == 0 && git.IsBehindUpstream(host, e.Repo, e.Branch) {
			return "merged"
		}
		if !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > opencode.StaleThreshold {
			return "stale"
		}
		return "idle"
	}

	// Has visible changes in diff.
	if e.Branch == "" {
		return "dirty"
	}
	unique := git.UniqueCommitCount(host, e.Repo, e.Branch)
	if unique == 0 {
		return "dirty" // changes with no commits = must be uncommitted
	}
	if git.IsMerged(host, e.Repo, e.Branch) {
		// Branch is merged but diff is non-empty. Could be squash-merge
		// artifact or new uncommitted work. Check to protect from auto-removal.
		if git.HasUncommittedChanges(host, e.Dir) {
			return "dirty"
		}
		return "merged"
	}
	return "committed"
}

// classifyFromResult classifies a worktree using pre-computed git results
// (from a batch SSH call) instead of making individual git calls.
// Unlike classifyStatus, this does NOT check Attached or working status —
// callers must handle those cases before calling this function.
func classifyFromResult(e worktree.Entry, r git.ClassifyResult) string {
	if !r.HasDiff {
		// No visible changes — cannot be dirty.
		if e.Branch == "" {
			if e.SessionID == "" {
				return "empty"
			}
			if !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > opencode.StaleThreshold {
				return "stale"
			}
			return "idle"
		}
		if r.Unique > 0 && r.Merged {
			return "merged"
		}
		// Session check before behind: a worktree with no session never had
		// work to merge, so "behind upstream" is irrelevant.
		if e.SessionID == "" {
			return "empty"
		}
		if r.Unique == 0 && r.Behind {
			return "merged"
		}
		if !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > opencode.StaleThreshold {
			return "stale"
		}
		return "idle"
	}

	// Has visible changes in diff.
	if e.Branch == "" {
		return "dirty"
	}
	if r.Unique == 0 {
		return "dirty"
	}
	if r.Merged {
		if r.HasUncommitted {
			return "dirty"
		}
		return "merged"
	}
	return "committed"
}

// isRemovable returns true if a status indicates the worktree is safe to remove.
func isRemovable(status string) bool {
	return status == "merged" || status == "stale" || status == "empty"
}

// rmResult holds the outcome of attempting to remove a single worktree.
type rmResult struct {
	entry  worktree.Entry
	status string
	errMsg string
}

// sortRmResults sorts removed entries first, then by most recent activity.
func sortRmResults(results []rmResult) {
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
		return false
	})
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

	statuses := classifyAll(all, pulled)

	// Remove sequentially
	var results []rmResult
	var removeCount int

	for i, e := range all {
		status := statuses[i]
		var errMsg string

		if isRemovable(status) {
			host := hostFor(e)
			if err := git.WorktreeRemove(host, e.Repo, e.Branch, e.Dir); err != nil {
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
	if err := git.WorktreeForceRemove(host, entry.Repo, entry.Branch, entry.Dir); err != nil {
		die("%v", err)
	}
	display.PrintTable([]display.Row{{
		Entry:  entry,
		Status: "removed",
	}}, opencode.ServerPort())
}
